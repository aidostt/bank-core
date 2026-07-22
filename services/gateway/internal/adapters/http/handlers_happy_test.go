package http

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// bodyReq drives a route with a concrete JSON body (the shared request helper
// always sends "{}", which only exercises the validation branches).
func (f *gwFixture) bodyReq(method, path, token, body string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
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

// Every route's success branch through the fake backends: covers handlers.go
// translation + dto.go marshaling.
func TestHandlersHappyPaths(t *testing.T) {
	f := newGateway(t)
	cust := f.token(t, "cust-1", []string{"customer"}, time.Hour)
	admin := f.token(t, "adm-1", []string{"admin"}, time.Hour)
	idem := map[string]string{"Idempotency-Key": "k-1"}
	id := "00000000-0000-7000-8000-000000000042"

	cases := []struct {
		name, method, path, token, body string
		headers                         map[string]string
		wantMax                         int
	}{
		{"register", "POST", "/v1/auth/register", "", `{"email":"a@b.kz","password":"password1","name":"A"}`, nil, 299},
		{"login", "POST", "/v1/auth/login", "", `{"email":"a@b.kz","password":"password1"}`, nil, 299},
		{"refresh", "POST", "/v1/auth/refresh", "", `{"refresh_token":"r"}`, nil, 299},
		{"logout", "POST", "/v1/auth/logout", "", `{"refresh_token":"r"}`, nil, 299},
		{"me", "GET", "/v1/customers/me", cust, "", nil, 299},
		{"open account", "POST", "/v1/accounts", cust, `{"currency":"KZT"}`, nil, 299},
		{"list accounts", "GET", "/v1/accounts", cust, "", nil, 299},
		{"transactions", "GET", "/v1/accounts/" + id + "/transactions?page_size=10", cust, "", nil, 299},
		{"topup", "POST", "/v1/topups", cust, `{"account_id":"` + id + `","minor_units":1000,"currency":"KZT"}`, idem, 299},
		{"transfer p2p", "POST", "/v1/transfers", cust, `{"type":"P2P","from_account_id":"` + id + `","to_account_number":"KZ1","minor_units":100,"currency":"KZT"}`, idem, 299},
		{"get transfer", "GET", "/v1/transfers/" + id, cust, "", nil, 299},
		{"list transfers", "GET", "/v1/transfers?page_size=5", cust, "", nil, 299},
		{"rates", "GET", "/v1/rates", cust, "", nil, 299},
		{"admin list", "GET", "/v1/admin/customers/" + id + "/accounts", admin, "", nil, 299},
		{"freeze", "POST", "/v1/admin/accounts/" + id + "/freeze", admin, `{"reason":"x"}`, nil, 299},
		{"unfreeze", "POST", "/v1/admin/accounts/" + id + "/unfreeze", admin, "", nil, 299},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := f.bodyReq(c.method, c.path, c.token, c.body, c.headers)
			if w.Code > c.wantMax {
				t.Fatalf("%s %s → %d (body %s)", c.method, c.path, w.Code, w.Body.String())
			}
		})
	}
}

// A bad transfer type and a malformed body exercise the 400 branches.
func TestHandlersBadInput(t *testing.T) {
	f := newGateway(t)
	cust := f.token(t, "cust-1", []string{"customer"}, time.Hour)
	idem := map[string]string{"Idempotency-Key": "k-1"}

	if w := f.bodyReq("POST", "/v1/transfers", cust, `{"type":"CHEQUE","from_account_id":"a","minor_units":1,"currency":"KZT"}`, idem); w.Code != http.StatusBadRequest {
		t.Fatalf("bad type → %d", w.Code)
	}
	if w := f.bodyReq("POST", "/v1/accounts", cust, `{not json`, nil); w.Code != http.StatusBadRequest {
		t.Fatalf("malformed body → %d", w.Code)
	}
}

// CORS preflight short-circuits before auth.
func TestCORSPreflight(t *testing.T) {
	f := newGateway(t)
	req := httptest.NewRequest(http.MethodOptions, "/v1/accounts", nil)
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("preflight → %d", w.Code)
	}
}
