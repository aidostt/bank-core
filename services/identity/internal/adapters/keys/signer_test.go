package keys

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestSignAndVerifyClaims(t *testing.T) {
	s, err := Load(t.TempDir(), "test-issuer")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := s.SignAccess("user-1", "family-1", []string{"customer"}, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	tok, err := jwt.Parse(raw, func(tk *jwt.Token) (any, error) {
		if tk.Method.Alg() != "RS256" {
			t.Fatalf("alg = %s", tk.Method.Alg())
		}
		if tk.Header["kid"] == "" {
			t.Fatal("kid header missing")
		}
		return s.Public(), nil
	})
	if err != nil || !tok.Valid {
		t.Fatalf("parse: %v", err)
	}
	claims := tok.Claims.(jwt.MapClaims)
	if claims["sub"] != "user-1" || claims["sid"] != "family-1" || claims["iss"] != "test-issuer" {
		t.Fatalf("claims: %+v", claims)
	}
}

func TestKeyPersistsAcrossLoads(t *testing.T) {
	dir := t.TempDir()
	s1, err := Load(dir, "iss")
	if err != nil {
		t.Fatal(err)
	}
	s2, err := Load(dir, "iss")
	if err != nil {
		t.Fatal(err)
	}
	if s1.kid != s2.kid {
		t.Fatalf("kid changed across restarts: %s vs %s", s1.kid, s2.kid)
	}
}

func TestJWKSShape(t *testing.T) {
	s, err := Load(t.TempDir(), "iss")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Keys []map[string]string `json:"keys"`
	}
	if err := json.Unmarshal(s.JWKS(), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Keys) != 1 {
		t.Fatalf("want 1 key, got %d", len(doc.Keys))
	}
	k := doc.Keys[0]
	for _, f := range []string{"kty", "use", "alg", "kid", "n", "e"} {
		if k[f] == "" {
			t.Errorf("jwks key missing %q", f)
		}
	}
}
