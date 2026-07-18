package domain

import (
	"testing"
	"time"
)

func TestValidateRegistration(t *testing.T) {
	cases := []struct {
		name    string
		email   string
		pass    string
		user    string
		wantErr error
	}{
		{"ok", "a@b.kz", "longenough", "Alice", nil},
		{"bad email", "not-an-email", "longenough", "Alice", ErrInvalidEmail},
		{"short password", "a@b.kz", "short", "Alice", ErrWeakPassword},
		{"empty name", "a@b.kz", "longenough", "", ErrEmptyName},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := ValidateRegistration(c.email, c.pass, c.user); err != c.wantErr {
				t.Fatalf("got %v, want %v", err, c.wantErr)
			}
		})
	}
}

func TestDecideRefresh(t *testing.T) {
	now := time.Now()
	revoked := now.Add(-time.Minute)
	cases := []struct {
		name string
		s    Session
		want RefreshDecision
	}{
		{"live token rotates", Session{ExpiresAt: now.Add(time.Hour)}, DecisionRotate},
		{"rotated token is reuse attack", Session{RevokedAt: &revoked, RotatedTo: "next-id"}, DecisionReuse},
		{"logged-out token denied", Session{RevokedAt: &revoked}, DecisionDenied},
		{"expired token", Session{ExpiresAt: now.Add(-time.Hour)}, DecisionExpired},
		{"revoked wins over expired", Session{RevokedAt: &revoked, RotatedTo: "x", ExpiresAt: now.Add(-time.Hour)}, DecisionReuse},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := DecideRefresh(c.s, now); got != c.want {
				t.Fatalf("got %v, want %v", got, c.want)
			}
		})
	}
}
