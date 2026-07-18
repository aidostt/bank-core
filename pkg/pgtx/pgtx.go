// Package pgtx: pgx pool bootstrap with startup retry, a small TxManager
// (ADR-0005 — transactions are controlled in the app layer), and the
// embedded-migrations runner.
package pgtx

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Connect opens a pool and pings it, retrying for up to ~60s so services
// survive compose startup ordering without healthcheck gymnastics.
func Connect(ctx context.Context, dsn string, log *slog.Logger) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("pgx pool: %w", err)
	}
	deadline := time.Now().Add(60 * time.Second)
	backoff := 250 * time.Millisecond
	for {
		pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		err = pool.Ping(pingCtx)
		cancel()
		if err == nil {
			return pool, nil
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			pool.Close()
			return nil, fmt.Errorf("db unreachable: %w", err)
		}
		log.Warn("db not ready, retrying", slog.String("error", err.Error()))
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			pool.Close()
			return nil, ctx.Err()
		}
		if backoff < 5*time.Second {
			backoff *= 2
		}
	}
}

// TxManager runs a function inside a database transaction.
type TxManager struct {
	pool *pgxpool.Pool
}

func NewTxManager(pool *pgxpool.Pool) *TxManager {
	return &TxManager{pool: pool}
}

func (m *TxManager) Pool() *pgxpool.Pool { return m.pool }

// WithTx begins a transaction (Read Committed — ADR-0007), calls fn and
// commits; any error rolls back.
func (m *TxManager) WithTx(ctx context.Context, fn func(ctx context.Context, tx pgx.Tx) error) error {
	tx, err := m.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() {
		if rbErr := tx.Rollback(ctx); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
			// Rollback after commit is a no-op; anything else is worth surfacing in logs by caller.
			_ = rbErr
		}
	}()
	if err := fn(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}
