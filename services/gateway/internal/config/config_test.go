package config

import "testing"

func TestLoad(t *testing.T) {
	t.Setenv("GATEWAY_REDIS_ADDR", "x")
	t.Setenv("GATEWAY_JWKS_URL", "x")
	t.Setenv("GATEWAY_IDENTITY_ADDR", "x")
	t.Setenv("GATEWAY_ACCOUNT_ADDR", "x")
	t.Setenv("GATEWAY_TRANSFER_ADDR", "x")
	if _, err := Load(); err != nil {
		t.Fatalf("Load with all required set: %v", err)
	}
}

func TestLoadMissingRequired(t *testing.T) {
	t.Setenv("GATEWAY_JWKS_URL", "x")
	t.Setenv("GATEWAY_IDENTITY_ADDR", "x")
	t.Setenv("GATEWAY_ACCOUNT_ADDR", "x")
	t.Setenv("GATEWAY_TRANSFER_ADDR", "x")
	t.Setenv("GATEWAY_REDIS_ADDR", "")
	if _, err := Load(); err == nil {
		t.Fatal("want error when GATEWAY_REDIS_ADDR missing")
	}
}
