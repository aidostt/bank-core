package http

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestJWKSAndHealth(t *testing.T) {
	jwks := []byte(`{"keys":[{"kty":"RSA","kid":"abc"}]}`)
	srv := NewServer(":0", jwks, nil) // pool only used by /readyz

	// JWKS endpoint serves the document with a cacheable content type.
	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/.well-known/jwks.json", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("jwks status %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("jwks content type %q", ct)
	}
	if rec.Body.String() != string(jwks) {
		t.Fatalf("jwks body mismatch: %s", rec.Body.String())
	}

	// Liveness never touches the database.
	rec = httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("healthz status %d", rec.Code)
	}
}
