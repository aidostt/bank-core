//go:build integration

package app

import (
	"context"
	"testing"
	"time"

	"github.com/aidostt/bank-core/pkg/apperr"
	"github.com/aidostt/bank-core/pkg/logging"
	"github.com/aidostt/bank-core/pkg/pgtx"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/aidostt/bank-core/services/identity/internal/adapters/keys"
	"github.com/aidostt/bank-core/services/identity/internal/adapters/postgres"
	"github.com/aidostt/bank-core/services/identity/migrations"
)

func startPostgres(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	pg, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("identity_db"),
		tcpostgres.WithUsername("identity_user"),
		tcpostgres.WithPassword("identity_pass"),
		testcontainers.WithWaitStrategy(wait.ForListeningPort("5432/tcp")),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pg.Terminate(context.Background()) })

	dsn, err := pg.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	// The container reports ready slightly before accepting TCP auth; the
	// migration runner retries via database ping inside pgtx.Connect below.
	deadline := time.Now().Add(30 * time.Second)
	for {
		if err = pgtx.Migrate(dsn, migrations.FS, "."); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("migrate: %v", err)
		}
		time.Sleep(500 * time.Millisecond)
	}
	pool, err := pgtx.Connect(ctx, dsn, logging.New("test"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// Full ADR-0011 scenario: register → login → refresh → reuse attack kills
// the session family (identity-service DoD).
func TestRegisterLoginRefreshReuseAttack(t *testing.T) {
	pool := startPostgres(t)
	ctx := context.Background()
	signer, err := keys.Load(t.TempDir(), "test-issuer")
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(postgres.NewStore(pool), signer, 15*time.Minute, 30*24*time.Hour, logging.New("test"))

	// register
	u, err := svc.Register(ctx, "alice@example.kz", "password-123", "Alice", "+7 700 000 00 00")
	if err != nil {
		t.Fatal(err)
	}
	if u.Roles[0] != "customer" {
		t.Fatalf("default role = %v", u.Roles)
	}

	// duplicate email
	if _, err := svc.Register(ctx, "alice@example.kz", "password-123", "Alice", ""); apperr.CodeOf(err) != apperr.CodeAlreadyExists {
		t.Fatalf("duplicate email: got %v", err)
	}

	// login
	pair, err := svc.Login(ctx, "alice@example.kz", "password-123", "127.0.0.1", "go-test")
	if err != nil {
		t.Fatal(err)
	}
	if pair.AccessToken == "" || pair.RefreshToken == "" {
		t.Fatal("empty tokens")
	}

	// wrong password
	if _, err := svc.Login(ctx, "alice@example.kz", "nope-nope-nope", "", ""); apperr.CodeOf(err) != apperr.CodeUnauthenticated {
		t.Fatalf("wrong password: got %v", err)
	}

	// refresh rotates
	pair2, err := svc.Refresh(ctx, pair.RefreshToken, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if pair2.RefreshToken == pair.RefreshToken {
		t.Fatal("refresh token not rotated")
	}

	// reuse of the rotated token = attack → whole family revoked
	if _, err := svc.Refresh(ctx, pair.RefreshToken, "", ""); apperr.CodeOf(err) != apperr.CodeUnauthenticated {
		t.Fatalf("reuse: got %v", err)
	}

	// the latest (legitimately rotated) token must now be dead too
	if _, err := svc.Refresh(ctx, pair2.RefreshToken, "", ""); apperr.CodeOf(err) != apperr.CodeUnauthenticated {
		t.Fatalf("family not revoked: got %v", err)
	}

	// sessions list is empty after the family kill
	sessions, err := svc.ListSessions(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 0 {
		t.Fatalf("want 0 active sessions, got %d", len(sessions))
	}
}

func TestLogoutRevokesFamily(t *testing.T) {
	pool := startPostgres(t)
	ctx := context.Background()
	signer, err := keys.Load(t.TempDir(), "test-issuer")
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(postgres.NewStore(pool), signer, 15*time.Minute, 30*24*time.Hour, logging.New("test"))

	if _, err := svc.Register(ctx, "bob@example.kz", "password-123", "Bob", ""); err != nil {
		t.Fatal(err)
	}
	pair, err := svc.Login(ctx, "bob@example.kz", "password-123", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Logout(ctx, pair.RefreshToken); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Refresh(ctx, pair.RefreshToken, "", ""); apperr.CodeOf(err) != apperr.CodeUnauthenticated {
		t.Fatalf("refresh after logout: got %v", err)
	}
	// idempotent
	if err := svc.Logout(ctx, pair.RefreshToken); err != nil {
		t.Fatal(err)
	}
}
