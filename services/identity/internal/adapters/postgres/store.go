// Package postgres wraps the sqlc-generated queries with transaction
// control (ADR-0005).
package postgres

import (
	"context"
	"errors"

	"github.com/aidostt/bank-core/pkg/pgtx"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aidostt/bank-core/services/identity/internal/adapters/postgres/db"
)

type Store struct {
	tx      *pgtx.TxManager
	queries *db.Queries
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{tx: pgtx.NewTxManager(pool), queries: db.New(pool)}
}

// Queries returns pool-backed (auto-commit) queries for single reads.
func (s *Store) Queries() *db.Queries { return s.queries }

// WithTx runs fn with transaction-scoped queries.
func (s *Store) WithTx(ctx context.Context, fn func(ctx context.Context, q *db.Queries) error) error {
	return s.tx.WithTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		return fn(ctx, s.queries.WithTx(tx))
	})
}

func IsUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
