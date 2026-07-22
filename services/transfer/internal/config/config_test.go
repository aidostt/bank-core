package config

import "testing"

func TestLoad(t *testing.T) {
	t.Setenv("TRANSFER_DB_DSN", "x")
	t.Setenv("TRANSFER_KAFKA_BROKERS", "x")
	t.Setenv("TRANSFER_LEDGER_ADDR", "x")
	t.Setenv("TRANSFER_ACCOUNT_ADDR", "x")
	if _, err := Load(); err != nil {
		t.Fatalf("Load with all required set: %v", err)
	}
}

func TestLoadMissingRequired(t *testing.T) {
	t.Setenv("TRANSFER_KAFKA_BROKERS", "x")
	t.Setenv("TRANSFER_LEDGER_ADDR", "x")
	t.Setenv("TRANSFER_ACCOUNT_ADDR", "x")
	t.Setenv("TRANSFER_DB_DSN", "")
	if _, err := Load(); err == nil {
		t.Fatal("want error when TRANSFER_DB_DSN missing")
	}
}
