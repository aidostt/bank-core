//go:build integration

package grpc_test

import (
	"context"
	"testing"
	"time"

	identityv1 "github.com/aidostt/bank-core/gen/go/bank/identity/v1"
	"github.com/aidostt/bank-core/pkg/grpcx"
	"github.com/aidostt/bank-core/pkg/logging"
	"github.com/aidostt/bank-core/pkg/pgtx"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	grpcadapter "github.com/aidostt/bank-core/services/identity/internal/adapters/grpc"
	"github.com/aidostt/bank-core/services/identity/internal/adapters/keys"
	"github.com/aidostt/bank-core/services/identity/internal/adapters/postgres"
	"github.com/aidostt/bank-core/services/identity/internal/app"
	"github.com/aidostt/bank-core/services/identity/migrations"
)

func newServer(t *testing.T) *grpcadapter.Server {
	t.Helper()
	ctx := context.Background()
	pg, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("identity_db"), tcpostgres.WithUsername("id"), tcpostgres.WithPassword("id"),
		testcontainers.WithWaitStrategy(wait.ForListeningPort("5432/tcp")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pg.Terminate(context.Background()) })
	dsn, err := pg.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		if err = pgtx.Migrate(dsn, migrations.FS, "."); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("migrate: %v", err)
		}
		time.Sleep(500 * time.Millisecond)
	}
	pool, err := pgtx.Connect(ctx, dsn, logging.New("test"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	signer, err := keys.Load(t.TempDir(), "test-issuer")
	if err != nil {
		t.Fatal(err)
	}
	svc := app.NewService(postgres.NewStore(pool), signer, 15*time.Minute, 30*24*time.Hour, logging.New("test"))
	return grpcadapter.NewServer(svc)
}

func TestIdentityGRPCSurface(t *testing.T) {
	srv := newServer(t)
	ctx := context.Background()

	// register → login → refresh, then authenticated GetMe/ListSessions.
	reg, err := srv.Register(ctx, &identityv1.RegisterRequest{
		Email: "grpc@demo.kz", Password: "password-123", Name: "GRPC", Phone: "+7",
	})
	if err != nil {
		t.Fatal(err)
	}
	uid := reg.GetUser().GetId()
	if uid == "" || reg.GetUser().GetRoles()[0] != "customer" {
		t.Fatalf("register: %+v", reg.GetUser())
	}
	// duplicate email → AlreadyExists
	if _, err := srv.Register(ctx, &identityv1.RegisterRequest{Email: "grpc@demo.kz", Password: "password-123", Name: "x"}); status.Code(err) != codes.AlreadyExists {
		t.Fatalf("dup email: %v", err)
	}

	login, err := srv.Login(ctx, &identityv1.LoginRequest{Email: "grpc@demo.kz", Password: "password-123"})
	if err != nil || login.GetAccessToken() == "" {
		t.Fatalf("login: %v", err)
	}
	// wrong password → Unauthenticated
	if _, err := srv.Login(ctx, &identityv1.LoginRequest{Email: "grpc@demo.kz", Password: "nope"}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("wrong pw: %v", err)
	}

	ref, err := srv.Refresh(ctx, &identityv1.RefreshRequest{RefreshToken: login.GetRefreshToken()})
	if err != nil || ref.GetRefreshToken() == login.GetRefreshToken() {
		t.Fatalf("refresh did not rotate: %v", err)
	}

	authed := grpcx.ContextWithClaims(ctx, grpcx.Claims{CustomerID: uid, Roles: []string{"customer"}})
	me, err := srv.GetMe(authed, &identityv1.GetMeRequest{})
	if err != nil || me.GetUser().GetEmail() != "grpc@demo.kz" {
		t.Fatalf("getme: %v", err)
	}
	sessions, err := srv.ListSessions(authed, &identityv1.ListSessionsRequest{})
	if err != nil || len(sessions.GetSessions()) == 0 {
		t.Fatalf("sessions: %v", err)
	}
	if _, err := srv.RevokeSession(authed, &identityv1.RevokeSessionRequest{SessionId: sessions.GetSessions()[0].GetId()}); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	// GetMe without claims → Unauthenticated
	if _, err := srv.GetMe(ctx, &identityv1.GetMeRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("anon getme: %v", err)
	}
	// logout is idempotent
	if _, err := srv.Logout(ctx, &identityv1.LogoutRequest{RefreshToken: ref.GetRefreshToken()}); err != nil {
		t.Fatalf("logout: %v", err)
	}
}
