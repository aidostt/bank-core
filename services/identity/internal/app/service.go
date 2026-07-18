// Package app implements identity use cases: registration, login, refresh
// rotation with reuse detection, session management (ADR-0011).
package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/aidostt/bank-core/pkg/apperr"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aidostt/bank-core/services/identity/internal/adapters/postgres"
	"github.com/aidostt/bank-core/services/identity/internal/adapters/postgres/db"
	"github.com/aidostt/bank-core/services/identity/internal/domain"
)

// TokenSigner is implemented by adapters/keys.
type TokenSigner interface {
	SignAccess(userID, familyID string, roles []string, ttl time.Duration) (string, error)
}

type Service struct {
	store      *postgres.Store
	signer     TokenSigner
	accessTTL  time.Duration
	refreshTTL time.Duration
	log        *slog.Logger
}

func NewService(store *postgres.Store, signer TokenSigner, accessTTL, refreshTTL time.Duration, log *slog.Logger) *Service {
	return &Service{store: store, signer: signer, accessTTL: accessTTL, refreshTTL: refreshTTL, log: log}
}

type UserView struct {
	ID        string
	Email     string
	Name      string
	Phone     string
	Roles     []string
	CreatedAt time.Time
}

type TokenPair struct {
	AccessToken      string
	RefreshToken     string
	ExpiresInSeconds int64
	User             *UserView
}

func (s *Service) Register(ctx context.Context, email, password, name, phone string) (*UserView, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if err := domain.ValidateRegistration(email, password, name); err != nil {
		return nil, apperr.Wrap(apperr.CodeInvalidArgument, err.Error(), err)
	}
	hash, err := hashPassword(password)
	if err != nil {
		return nil, apperr.Wrap(apperr.CodeInternal, "hash password", err)
	}
	var view *UserView
	err = s.store.WithTx(ctx, func(ctx context.Context, q *db.Queries) error {
		u, err := q.CreateUser(ctx, db.CreateUserParams{Email: email, PasswordHash: hash, Name: name, Phone: phone})
		if err != nil {
			if postgres.IsUniqueViolation(err) {
				return apperr.Wrap(apperr.CodeAlreadyExists, "email already registered", domain.ErrEmailTaken)
			}
			return err
		}
		if err := q.AddRole(ctx, db.AddRoleParams{UserID: u.ID, Role: domain.RoleCustomer}); err != nil {
			return err
		}
		// M1 note: the customers.registered outbox event ships in M2
		// (docs/services/identity-service.md).
		view = &UserView{ID: u.ID, Email: u.Email, Name: u.Name, Phone: u.Phone,
			Roles: []string{domain.RoleCustomer}, CreatedAt: u.CreatedAt}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return view, nil
}

func (s *Service) Login(ctx context.Context, email, password, ip, userAgent string) (*TokenPair, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	q := s.store.Queries()
	u, err := q.GetUserByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperr.New(apperr.CodeUnauthenticated, "invalid credentials")
		}
		return nil, err
	}
	ok, err := verifyPassword(password, u.PasswordHash)
	if err != nil || !ok {
		return nil, apperr.New(apperr.CodeUnauthenticated, "invalid credentials")
	}
	roles, err := q.GetRoles(ctx, u.ID)
	if err != nil {
		return nil, err
	}

	familyID := uuid.NewString()
	raw, hash, err := newRefreshToken()
	if err != nil {
		return nil, err
	}
	sess, err := q.CreateSession(ctx, db.CreateSessionParams{
		UserID:      u.ID,
		FamilyID:    familyID,
		RefreshHash: hash,
		ExpiresAt:   time.Now().Add(s.refreshTTL),
		Ip:          ip,
		UserAgent:   userAgent,
	})
	if err != nil {
		return nil, err
	}
	_ = sess
	access, err := s.signer.SignAccess(u.ID, familyID, roles, s.accessTTL)
	if err != nil {
		return nil, apperr.Wrap(apperr.CodeInternal, "sign token", err)
	}
	return &TokenPair{
		AccessToken:      access,
		RefreshToken:     raw,
		ExpiresInSeconds: int64(s.accessTTL.Seconds()),
		User: &UserView{ID: u.ID, Email: u.Email, Name: u.Name, Phone: u.Phone,
			Roles: roles, CreatedAt: u.CreatedAt},
	}, nil
}

func (s *Service) Refresh(ctx context.Context, rawToken, ip, userAgent string) (*TokenPair, error) {
	hash := hashRefreshToken(rawToken)
	q := s.store.Queries()
	sess, err := q.GetSessionByRefreshHash(ctx, hash)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperr.New(apperr.CodeUnauthenticated, "invalid refresh token")
		}
		return nil, err
	}

	switch domain.DecideRefresh(toDomainSession(sess), time.Now()) {
	case domain.DecisionReuse:
		// Theft signal: kill the whole family and audit-log it (ADR-0011).
		if err := q.RevokeSessionFamily(ctx, sess.FamilyID); err != nil {
			return nil, err
		}
		s.log.WarnContext(ctx, "refresh token reuse detected — session family revoked",
			slog.String("user.id", sess.UserID), slog.String("session.family", sess.FamilyID))
		return nil, apperr.New(apperr.CodeUnauthenticated, "refresh token reuse detected")
	case domain.DecisionDenied, domain.DecisionExpired:
		return nil, apperr.New(apperr.CodeUnauthenticated, "refresh token no longer valid")
	}

	raw, newHash, err := newRefreshToken()
	if err != nil {
		return nil, err
	}
	var access string
	err = s.store.WithTx(ctx, func(ctx context.Context, q *db.Queries) error {
		next, err := q.CreateSession(ctx, db.CreateSessionParams{
			UserID:      sess.UserID,
			FamilyID:    sess.FamilyID,
			RefreshHash: newHash,
			RotatedFrom: &sess.ID,
			ExpiresAt:   time.Now().Add(s.refreshTTL),
			Ip:          ip,
			UserAgent:   userAgent,
		})
		if err != nil {
			return err
		}
		// Guard against a concurrent rotation of the same token: exactly one
		// caller wins; the loser sees 0 rows and the family is revoked.
		n, err := q.MarkSessionRotated(ctx, db.MarkSessionRotatedParams{ID: sess.ID, RotatedTo: &next.ID})
		if err != nil {
			return err
		}
		if n == 0 {
			if err := q.RevokeSessionFamily(ctx, sess.FamilyID); err != nil {
				return err
			}
			return apperr.New(apperr.CodeUnauthenticated, "refresh token reuse detected")
		}
		roles, err := q.GetRoles(ctx, sess.UserID)
		if err != nil {
			return err
		}
		access, err = s.signer.SignAccess(sess.UserID, sess.FamilyID, roles, s.accessTTL)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &TokenPair{AccessToken: access, RefreshToken: raw, ExpiresInSeconds: int64(s.accessTTL.Seconds())}, nil
}

func (s *Service) Logout(ctx context.Context, rawToken string) error {
	sess, err := s.store.Queries().GetSessionByRefreshHash(ctx, hashRefreshToken(rawToken))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // logout is idempotent
		}
		return err
	}
	return s.store.Queries().RevokeSessionFamily(ctx, sess.FamilyID)
}

func (s *Service) GetMe(ctx context.Context, userID string) (*UserView, error) {
	if userID == "" {
		return nil, apperr.New(apperr.CodeUnauthenticated, "no caller identity")
	}
	q := s.store.Queries()
	u, err := q.GetUserByID(ctx, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperr.New(apperr.CodeNotFound, "user not found")
		}
		return nil, err
	}
	roles, err := q.GetRoles(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	return &UserView{ID: u.ID, Email: u.Email, Name: u.Name, Phone: u.Phone, Roles: roles, CreatedAt: u.CreatedAt}, nil
}

type SessionView struct {
	ID        string
	FamilyID  string
	CreatedAt time.Time
	ExpiresAt time.Time
	IP        string
	UserAgent string
}

func (s *Service) ListSessions(ctx context.Context, userID string) ([]SessionView, error) {
	rows, err := s.store.Queries().ListActiveSessions(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]SessionView, 0, len(rows))
	for _, r := range rows {
		out = append(out, SessionView{ID: r.ID, FamilyID: r.FamilyID, CreatedAt: r.CreatedAt,
			ExpiresAt: r.ExpiresAt, IP: r.Ip, UserAgent: r.UserAgent})
	}
	return out, nil
}

func (s *Service) RevokeSession(ctx context.Context, callerID, sessionID string) error {
	sess, err := s.store.Queries().GetSessionByID(ctx, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return apperr.New(apperr.CodeNotFound, "session not found")
		}
		return err
	}
	if sess.UserID != callerID {
		return apperr.New(apperr.CodeForbidden, "not your session")
	}
	return s.store.Queries().RevokeSessionFamily(ctx, sess.FamilyID)
}

func newRefreshToken() (raw string, hash []byte, err error) {
	buf := make([]byte, 32)
	if _, err = rand.Read(buf); err != nil {
		return "", nil, apperr.Wrap(apperr.CodeInternal, "entropy", err)
	}
	raw = base64.RawURLEncoding.EncodeToString(buf)
	return raw, hashRefreshTokenBytes(buf), nil
}

func hashRefreshToken(raw string) []byte {
	buf, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		// Not a token we issued; hash the raw string so lookup just misses.
		sum := sha256.Sum256([]byte(raw))
		return sum[:]
	}
	return hashRefreshTokenBytes(buf)
}

func hashRefreshTokenBytes(buf []byte) []byte {
	sum := sha256.Sum256(buf)
	return sum[:]
}

func toDomainSession(s db.Session) domain.Session {
	d := domain.Session{
		ID:        s.ID,
		UserID:    s.UserID,
		FamilyID:  s.FamilyID,
		ExpiresAt: s.ExpiresAt,
		RevokedAt: s.RevokedAt,
	}
	if s.RotatedTo != nil {
		d.RotatedTo = *s.RotatedTo
	}
	return d
}
