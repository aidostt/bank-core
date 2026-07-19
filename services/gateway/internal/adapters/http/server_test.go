package http

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	accountv1 "github.com/aidostt/bank-core/gen/go/bank/account/v1"
	identityv1 "github.com/aidostt/bank-core/gen/go/bank/identity/v1"
	transferv1 "github.com/aidostt/bank-core/gen/go/bank/transfer/v1"
	"github.com/aidostt/bank-core/pkg/apperr"
	"github.com/aidostt/bank-core/pkg/logging"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/aidostt/bank-core/services/gateway/internal/adapters/grpcclients"
	redisadapter "github.com/aidostt/bank-core/services/gateway/internal/adapters/redis"
	"github.com/aidostt/bank-core/services/gateway/internal/app"
	"github.com/aidostt/bank-core/services/gateway/internal/config"
)

// --- fake backends: minimal happy responses ---

type fakeIdentity struct{ identityv1.UnimplementedIdentityServiceServer }

func (fakeIdentity) Register(context.Context, *identityv1.RegisterRequest) (*identityv1.RegisterResponse, error) {
	return &identityv1.RegisterResponse{User: &identityv1.User{Id: "u1"}}, nil
}
func (fakeIdentity) Login(context.Context, *identityv1.LoginRequest) (*identityv1.LoginResponse, error) {
	return &identityv1.LoginResponse{AccessToken: "a", RefreshToken: "r", User: &identityv1.User{Id: "u1"}}, nil
}
func (fakeIdentity) Refresh(context.Context, *identityv1.RefreshRequest) (*identityv1.RefreshResponse, error) {
	return &identityv1.RefreshResponse{AccessToken: "a", RefreshToken: "r"}, nil
}
func (fakeIdentity) Logout(context.Context, *identityv1.LogoutRequest) (*identityv1.LogoutResponse, error) {
	return &identityv1.LogoutResponse{}, nil
}
func (fakeIdentity) GetMe(context.Context, *identityv1.GetMeRequest) (*identityv1.GetMeResponse, error) {
	return &identityv1.GetMeResponse{User: &identityv1.User{Id: "u1"}}, nil
}

type fakeAccount struct{ accountv1.UnimplementedAccountServiceServer }

func (fakeAccount) OpenAccount(context.Context, *accountv1.OpenAccountRequest) (*accountv1.OpenAccountResponse, error) {
	return &accountv1.OpenAccountResponse{Account: &accountv1.Account{Id: "a1"}}, nil
}
func (fakeAccount) ListAccountsByCustomer(context.Context, *accountv1.ListAccountsByCustomerRequest) (*accountv1.ListAccountsByCustomerResponse, error) {
	return &accountv1.ListAccountsByCustomerResponse{}, nil
}
func (fakeAccount) ListTransactions(context.Context, *accountv1.ListTransactionsRequest) (*accountv1.ListTransactionsResponse, error) {
	return &accountv1.ListTransactionsResponse{}, nil
}
func (fakeAccount) Freeze(context.Context, *accountv1.FreezeRequest) (*accountv1.FreezeResponse, error) {
	return &accountv1.FreezeResponse{Account: &accountv1.Account{Id: "a1", Status: "FROZEN"}}, nil
}
func (fakeAccount) Unfreeze(context.Context, *accountv1.UnfreezeRequest) (*accountv1.UnfreezeResponse, error) {
	return &accountv1.UnfreezeResponse{Account: &accountv1.Account{Id: "a1", Status: "ACTIVE"}}, nil
}

type fakeTransfer struct{ transferv1.UnimplementedTransferServiceServer }

func (fakeTransfer) CreateTransfer(context.Context, *transferv1.CreateTransferRequest) (*transferv1.CreateTransferResponse, error) {
	return &transferv1.CreateTransferResponse{Transfer: &transferv1.TransferView{
		Id: "t1", State: transferv1.TransferState_TRANSFER_STATE_COMPLETED,
	}}, nil
}
func (fakeTransfer) GetTransfer(context.Context, *transferv1.GetTransferRequest) (*transferv1.GetTransferResponse, error) {
	return &transferv1.GetTransferResponse{Transfer: &transferv1.TransferView{Id: "t1"}}, nil
}
func (fakeTransfer) ListTransfers(context.Context, *transferv1.ListTransfersRequest) (*transferv1.ListTransfersResponse, error) {
	return &transferv1.ListTransfersResponse{}, nil
}
func (fakeTransfer) GetRates(context.Context, *transferv1.GetRatesRequest) (*transferv1.GetRatesResponse, error) {
	return &transferv1.GetRatesResponse{}, nil
}

// --- fixture ---

type gwFixture struct {
	router *gin.Engine
	key    *rsa.PrivateKey
	kid    string
	jwks   *httptest.Server
}

func newGateway(t *testing.T) *gwFixture {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pubDER, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	sum := sha256.Sum256(pubDER)
	kid := hex.EncodeToString(sum[:8])

	jwksSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "use": "sig", "alg": "RS256", "kid": kid,
			"n": base64.RawURLEncoding.EncodeToString(key.PublicKey.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.PublicKey.E)).Bytes()),
		}}})
	}))
	t.Cleanup(jwksSrv.Close)

	// fake backends on loopback
	serve := func(register func(s *grpc.Server)) string {
		lis, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		srv := grpc.NewServer()
		register(srv)
		go func() { _ = srv.Serve(lis) }()
		t.Cleanup(srv.Stop)
		return lis.Addr().String()
	}
	identityAddr := serve(func(s *grpc.Server) { identityv1.RegisterIdentityServiceServer(s, fakeIdentity{}) })
	accountAddr := serve(func(s *grpc.Server) { accountv1.RegisterAccountServiceServer(s, fakeAccount{}) })
	transferAddr := serve(func(s *grpc.Server) { transferv1.RegisterTransferServiceServer(s, fakeTransfer{}) })

	dial := func(addr string) *grpc.ClientConn {
		conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		return conn
	}
	clients := &grpcclients.Clients{
		Identity: identityv1.NewIdentityServiceClient(dial(identityAddr)),
		Account:  accountv1.NewAccountServiceClient(dial(accountAddr)),
		Transfer: transferv1.NewTransferServiceClient(dial(transferAddr)),
	}

	mr := miniredis.RunT(t)
	limiter := redisadapter.NewLimiter(mr.Addr())
	t.Cleanup(func() { _ = limiter.Close() })

	cfg := config.Config{
		HTTPAddr: ":0", JWTIssuer: "test-issuer",
		RateLimitReads: 10, RateLimitWrites: 2,
	}
	jwksCache := app.NewJWKSCache(jwksSrv.URL, "test-issuer")
	if err := jwksCache.WarmUp(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	server := NewServer(cfg, jwksCache, clients, limiter, logging.New("gateway-test"))
	return &gwFixture{router: server.Router(), key: key, kid: kid, jwks: jwksSrv}
}

func (f *gwFixture) token(t *testing.T, sub string, roles []string, exp time.Duration) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss": "test-issuer", "sub": sub, "sid": "sid-1", "roles": roles,
		"iat": time.Now().Unix(), "exp": time.Now().Add(exp).Unix(),
	})
	tok.Header["kid"] = f.kid
	raw, err := tok.SignedString(f.key)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func (f *gwFixture) request(method, path, token string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	return w
}

// pathFor turns a route pattern into a concrete URL.
func pathFor(pattern string) string {
	return strings.ReplaceAll(pattern, ":id", "00000000-0000-7000-8000-000000000042")
}

// The RBAC table test (api-gateway doc DoD): every route × every role.
func TestAuthzTable(t *testing.T) {
	f := newGateway(t)
	roleTokens := map[string]string{
		"customer": f.token(t, "cust-1", []string{"customer"}, time.Hour),
		"support":  f.token(t, "sup-1", []string{"support"}, time.Hour),
		"admin":    f.token(t, "adm-1", []string{"admin"}, time.Hour),
	}
	idem := map[string]string{"Idempotency-Key": "test-key"}

	for _, route := range app.Table() {
		url := pathFor(route.Path)
		// unauthenticated
		w := f.request(route.Method, url, "", idem)
		if len(route.Roles) == 0 {
			if w.Code == http.StatusUnauthorized || w.Code == http.StatusForbidden {
				t.Errorf("%s %s: public route rejected anon (%d)", route.Method, route.Path, w.Code)
			}
		} else if w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s: anon got %d, want 401", route.Method, route.Path, w.Code)
		}
		// each role
		for role, tok := range roleTokens {
			w := f.request(route.Method, url, tok, idem)
			allowed := app.Allowed(route, []string{role})
			if allowed && (w.Code == http.StatusUnauthorized || w.Code == http.StatusForbidden) {
				t.Errorf("%s %s: %s got %d, want allowed", route.Method, route.Path, role, w.Code)
			}
			if !allowed && w.Code != http.StatusForbidden {
				t.Errorf("%s %s: %s got %d, want 403", route.Method, route.Path, role, w.Code)
			}
		}
	}
}

func TestIdempotencyKeyEnforced(t *testing.T) {
	f := newGateway(t)
	tok := f.token(t, "cust-1", []string{"customer"}, time.Hour)

	w := f.request("POST", "/v1/transfers", tok, nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("missing key: %d", w.Code)
	}
	var p Problem
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if p.Code != string(apperr.CodeIdempotencyKeyRequired) {
		t.Fatalf("code = %s", p.Code)
	}

	// With the key the request passes through (fake completes → 400 is a
	// body-validation error, so send a valid body).
	req := httptest.NewRequest("POST", "/v1/transfers",
		strings.NewReader(`{"type":"P2P","from_account_id":"a","to_account_number":"n","minor_units":100,"currency":"KZT"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Idempotency-Key", "k-1")
	w2 := httptest.NewRecorder()
	f.router.ServeHTTP(w2, req)
	if w2.Code != http.StatusCreated {
		t.Fatalf("with key: %d body=%s", w2.Code, w2.Body.String())
	}
}

func TestJWTExpiryAndTampering(t *testing.T) {
	f := newGateway(t)

	expired := f.token(t, "cust-1", []string{"customer"}, -time.Minute)
	if w := f.request("GET", "/v1/customers/me", expired, nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("expired token: %d", w.Code)
	}

	valid := f.token(t, "cust-1", []string{"customer"}, time.Hour)
	parts := strings.Split(valid, ".")
	// Tamper with the payload, keep the signature.
	payload, _ := base64.RawURLEncoding.DecodeString(parts[1])
	tampered := strings.Replace(string(payload), "cust-1", "cust-2", 1)
	parts[1] = base64.RawURLEncoding.EncodeToString([]byte(tampered))
	if w := f.request("GET", "/v1/customers/me", strings.Join(parts, "."), nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("tampered token: %d", w.Code)
	}

	// HS256 token signed with the public modulus as secret — alg confusion
	// must be rejected.
	hs := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"iss": "test-issuer", "sub": "cust-1", "exp": time.Now().Add(time.Hour).Unix(),
	})
	hs.Header["kid"] = f.kid
	raw, _ := hs.SignedString(f.key.PublicKey.N.Bytes())
	if w := f.request("GET", "/v1/customers/me", raw, nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("alg-confusion token: %d", w.Code)
	}

	if w := f.request("GET", "/v1/customers/me", f.token(t, "cust-1", []string{"customer"}, time.Hour), nil); w.Code != http.StatusOK {
		t.Fatalf("valid token: %d", w.Code)
	}
}

func TestRateLimit(t *testing.T) {
	f := newGateway(t)
	tok := f.token(t, "cust-rl", []string{"customer"}, time.Hour)

	limited := 0
	for i := 0; i < 22; i++ { // ≥11 requests land in one 1s window
		w := f.request("GET", "/v1/rates", tok, nil)
		if w.Code == http.StatusTooManyRequests {
			limited++
			if w.Header().Get("Retry-After") == "" {
				t.Fatal("429 without Retry-After")
			}
		}
	}
	if limited == 0 {
		t.Fatal("rate limit never triggered after 22 rapid reads (limit 10 rps)")
	}
}

// The single problem+json mapping table (ADR-0018).
func TestProblemMapping(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)
	cases := []struct {
		code       apperr.Code
		wantStatus int
	}{
		{apperr.CodeInvalidArgument, 400},
		{apperr.CodeUnauthenticated, 401},
		{apperr.CodeForbidden, 403},
		{apperr.CodeNotFound, 404},
		{apperr.CodeAlreadyExists, 409},
		{apperr.CodeInsufficientFunds, 422},
		{apperr.CodeAccountFrozen, 422},
		{apperr.CodeLimitExceeded, 422},
		{apperr.CodeIdempotencyConflict, 422},
		{apperr.CodeIdempotencyKeyRequired, 400},
		{apperr.CodeRateLimited, 429},
		{apperr.CodeUpstreamUnavailable, 503},
		{apperr.CodeInternal, 500},
	}
	for _, c := range cases {
		t.Run(string(c.code), func(t *testing.T) {
			w := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(w)
			ctx.Request = httptest.NewRequest("GET", "/x", nil)
			writeProblem(ctx, apperr.New(c.code, "secret detail"))
			if w.Code != c.wantStatus {
				t.Fatalf("%s → %d, want %d", c.code, w.Code, c.wantStatus)
			}
			var p Problem
			if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
				t.Fatal(err)
			}
			if p.Code != string(c.code) {
				t.Fatalf("body code %s", p.Code)
			}
			if c.code == apperr.CodeInternal && p.Detail != "internal error" {
				t.Fatalf("internal detail leaked: %q", p.Detail)
			}
			if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/problem+json") {
				t.Fatalf("content type %s", ct)
			}
		})
	}
	// gRPC transport errors map through FromGRPC.
	w := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(w)
	ctx.Request = httptest.NewRequest("GET", "/x", nil)
	writeProblem(ctx, fmt.Errorf("wrapped: %w", apperr.New(apperr.CodeInsufficientFunds, "no funds")))
	if w.Code != 422 {
		t.Fatalf("wrapped apperr → %d", w.Code)
	}
}
