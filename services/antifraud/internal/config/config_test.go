package config

import "testing"

func TestLoad(t *testing.T) {
	t.Setenv("ANTIFRAUD_DB_DSN", "x")
	t.Setenv("ANTIFRAUD_KAFKA_BROKERS", "x")
	if _, err := Load(); err != nil {
		t.Fatalf("Load with all required set: %v", err)
	}
}

func TestLoadMissingRequired(t *testing.T) {
	t.Setenv("ANTIFRAUD_KAFKA_BROKERS", "x")
	t.Setenv("ANTIFRAUD_DB_DSN", "")
	if _, err := Load(); err == nil {
		t.Fatal("want error when ANTIFRAUD_DB_DSN missing")
	}
}
