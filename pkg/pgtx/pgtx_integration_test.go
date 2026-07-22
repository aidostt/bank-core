//go:build integration

package pgtx_test

import (
	"context"
	"embed"
	"errors"
	"testing"
	"time"

	"github.com/aidostt/bank-core/pkg/logging"
	"github.com/aidostt/bank-core/pkg/pgtx"
	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

//go:embed testdata/migrations/*.sql
var migrationsFS embed.FS

func dsn(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	pg, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("pgtx_db"),
		tcpostgres.WithUsername("pgtx_user"),
		tcpostgres.WithPassword("pgtx_pass"),
		testcontainers.WithWaitStrategy(wait.ForListeningPort("5432/tcp")),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pg.Terminate(context.Background()) })
	s, err := pg.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestMigrateConnectAndTx(t *testing.T) {
	d := dsn(t)
	log := logging.New("pgtx-test")

	// Migrate retries against a just-started container.
	deadline := time.Now().Add(30 * time.Second)
	for {
		if err := pgtx.Migrate(d, migrationsFS, "testdata/migrations"); err == nil {
			break
		} else if time.Now().After(deadline) {
			t.Fatalf("migrate: %v", err)
		}
		time.Sleep(500 * time.Millisecond)
	}
	// Idempotent second run (ErrNoChange swallowed).
	if err := pgtx.Migrate(d, migrationsFS, "testdata/migrations"); err != nil {
		t.Fatalf("second migrate: %v", err)
	}

	pool, err := pgtx.Connect(context.Background(), d, log)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	tm := pgtx.NewTxManager(pool)
	if tm.Pool() != pool {
		t.Fatal("Pool() mismatch")
	}

	ctx := context.Background()
	// Commit path.
	if err := tm.WithTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "INSERT INTO t (id, v) VALUES (1, 'a')")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// Rollback path: returning an error must undo the write.
	sentinel := errors.New("boom")
	if err := tm.WithTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "INSERT INTO t (id, v) VALUES (2, 'b')"); err != nil {
			return err
		}
		return sentinel
	}); !errors.Is(err, sentinel) {
		t.Fatalf("WithTx error = %v, want sentinel", err)
	}

	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM t").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("row count = %d, want 1 (rollback undid row 2)", count)
	}
}

func TestConnectBadDSN(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := pgtx.Connect(ctx, "postgres://x:y@127.0.0.1:1/nope?sslmode=disable", logging.New("t")); err == nil {
		t.Fatal("want error for unreachable db")
	}
}
