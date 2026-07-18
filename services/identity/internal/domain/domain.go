// Package domain holds identity's pure rules: registration validation and
// the refresh-rotation decision. No IO imports (ADR-0014, depguard).
package domain

import (
	"errors"
	"net/mail"
	"time"
	"unicode/utf8"
)

var (
	ErrInvalidEmail       = errors.New("invalid email")
	ErrWeakPassword       = errors.New("password must be at least 8 characters")
	ErrEmptyName          = errors.New("name is required")
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrSessionNotFound    = errors.New("session not found")
	ErrSessionExpired     = errors.New("session expired")
	ErrSessionRevoked     = errors.New("session revoked")
	ErrRefreshReuse       = errors.New("refresh token reuse detected")
	ErrEmailTaken         = errors.New("email already registered")
	ErrForbidden          = errors.New("forbidden")
)

const RoleCustomer = "customer"

func ValidateRegistration(email, password, name string) error {
	if _, err := mail.ParseAddress(email); err != nil {
		return ErrInvalidEmail
	}
	if utf8.RuneCountInString(password) < 8 {
		return ErrWeakPassword
	}
	if name == "" {
		return ErrEmptyName
	}
	return nil
}

// Session is the domain view of a refresh session row.
type Session struct {
	ID        string
	UserID    string
	FamilyID  string
	RotatedTo string // set when this token has been rotated (used)
	ExpiresAt time.Time
	RevokedAt *time.Time
}

// RefreshDecision classifies a presented refresh token (ADR-0011).
type RefreshDecision int

const (
	// DecisionRotate: token is live — rotate it and issue a new pair.
	DecisionRotate RefreshDecision = iota
	// DecisionReuse: token was already rotated — theft signal, revoke the
	// whole session family.
	DecisionReuse
	// DecisionDenied: token was revoked (logout / family kill).
	DecisionDenied
	// DecisionExpired: token outlived its TTL.
	DecisionExpired
)

func DecideRefresh(s Session, now time.Time) RefreshDecision {
	if s.RevokedAt != nil {
		if s.RotatedTo != "" {
			return DecisionReuse
		}
		return DecisionDenied
	}
	if now.After(s.ExpiresAt) {
		return DecisionExpired
	}
	return DecisionRotate
}
