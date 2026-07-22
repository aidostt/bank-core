package config

import (
	"testing"
	"time"
)

func TestLoaderRequiredAndDefaults(t *testing.T) {
	t.Setenv("PRESENT", "value")
	l := New()
	if got := l.String("PRESENT"); got != "value" {
		t.Fatalf("String(present) = %q", got)
	}
	l.String("MISSING_REQUIRED") // records a problem
	if got := l.StringDefault("ALSO_MISSING", "def"); got != "def" {
		t.Fatalf("StringDefault fallback = %q", got)
	}
	if got := l.StringDefault("PRESENT", "def"); got != "value" {
		t.Fatalf("StringDefault present = %q", got)
	}
	if err := l.Err(); err == nil {
		t.Fatal("want error for the missing required var")
	}
}

func TestLoaderIntAndDuration(t *testing.T) {
	t.Setenv("N", "42")
	t.Setenv("BAD_N", "notint")
	t.Setenv("D", "250ms")
	t.Setenv("BAD_D", "notdur")
	l := New()
	if l.Int("N", 7) != 42 {
		t.Fatal("Int parse")
	}
	if l.Int("UNSET_N", 7) != 7 {
		t.Fatal("Int default")
	}
	if l.Int("BAD_N", 7) != 7 {
		t.Fatal("Int bad falls back to default value")
	}
	if l.Duration("D", time.Second) != 250*time.Millisecond {
		t.Fatal("Duration parse")
	}
	if l.Duration("UNSET_D", time.Second) != time.Second {
		t.Fatal("Duration default")
	}
	_ = l.Duration("BAD_D", time.Second)
	// BAD_N and BAD_D both record problems.
	if err := l.Err(); err == nil {
		t.Fatal("want error for the malformed int/duration")
	}
}

func TestLoaderClean(t *testing.T) {
	if err := New().Err(); err != nil {
		t.Fatalf("empty loader should be clean: %v", err)
	}
}
