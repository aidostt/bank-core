package config

import "testing"

func TestLoad(t *testing.T) {
	t.Setenv("IDENTITY_DB_DSN", "x")
	t.Setenv("IDENTITY_KEYS_DIR", "x")
	t.Setenv("IDENTITY_KAFKA_BROKERS", "x")
	if _, err := Load(); err != nil {
		t.Fatalf("Load with all required set: %v", err)
	}
}

func TestLoadMissingRequired(t *testing.T) {
	t.Setenv("IDENTITY_KEYS_DIR", "x")
	t.Setenv("IDENTITY_KAFKA_BROKERS", "x")
	t.Setenv("IDENTITY_DB_DSN", "")
	if _, err := Load(); err == nil {
		t.Fatal("want error when IDENTITY_DB_DSN missing")
	}
}
