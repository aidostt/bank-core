// Package postgres wraps the sqlc-generated queries with transaction
// control (ADR-0005).
package postgres

import (
	"context"

	"github.com/aidostt/bank-core/pkg/pgtx"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aidostt/bank-core/services/account/internal/adapters/postgres/db"
)

type Store struct {
	tx      *pgtx.TxManager
	queries *db.Queries
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{tx: pgtx.NewTxManager(pool), queries: db.New(pool)}
}

func (s *Store) Queries() *db.Queries { return s.queries }

// WithTx exposes both the sqlc queries and the raw tx (outbox.Insert needs
// the latter — events commit atomically with the change, ADR-0009).
func (s *Store) WithTx(ctx context.Context, fn func(ctx context.Context, q *db.Queries, tx pgx.Tx) error) error {
	return s.tx.WithTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		return fn(ctx, s.queries.WithTx(tx), tx)
	})
}
