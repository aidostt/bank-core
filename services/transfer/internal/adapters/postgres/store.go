// Package postgres implements the transfer app ports (ADR-0005).
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/aidostt/bank-core/pkg/outbox"
	"github.com/aidostt/bank-core/pkg/pgtx"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aidostt/bank-core/services/transfer/internal/adapters/postgres/db"
	"github.com/aidostt/bank-core/services/transfer/internal/app"
	"github.com/aidostt/bank-core/services/transfer/internal/domain"
)

type Store struct {
	tx      *pgtx.TxManager
	queries *db.Queries
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{tx: pgtx.NewTxManager(pool), queries: db.New(pool)}
}

func (s *Store) Pool() *pgxpool.Pool { return s.tx.Pool() }

func (s *Store) WithinTx(ctx context.Context, fn func(ctx context.Context, tx app.StoreTx) error) error {
	return s.tx.WithTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		return fn(ctx, &storeTx{q: s.queries.WithTx(tx), raw: tx})
	})
}

func (s *Store) GetTransfer(ctx context.Context, id string) (*app.Transfer, error) {
	row, err := s.queries.GetTransfer(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, app.ErrNotFound
		}
		return nil, err
	}
	return transferFromRow(row), nil
}

func (s *Store) ListTransfersByCustomer(ctx context.Context, customerID string, limit int32, cursorCreatedAt time.Time, cursorID string) ([]*app.Transfer, error) {
	rows, err := s.queries.ListTransfersByCustomer(ctx, db.ListTransfersByCustomerParams{
		CustomerID: customerID,
		CursorTime: cursorCreatedAt,
		CursorID:   cursorID,
		PageLimit:  limit,
	})
	if err != nil {
		return nil, err
	}
	out := make([]*app.Transfer, 0, len(rows))
	for _, r := range rows {
		out = append(out, transferFromRow(r))
	}
	return out, nil
}

func (s *Store) GetLatestRate(ctx context.Context, pair string, at time.Time) (app.Rate, error) {
	row, err := s.queries.GetLatestRate(ctx, db.GetLatestRateParams{Pair: pair, ValidFrom: at})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return app.Rate{}, app.ErrNotFound
		}
		return app.Rate{}, err
	}
	return app.Rate{Pair: row.Pair, BuyMicros: row.BuyMicros, SellMicros: row.SellMicros, ValidFrom: row.ValidFrom}, nil
}

func (s *Store) ListLatestRates(ctx context.Context, at time.Time) ([]app.Rate, error) {
	rows, err := s.queries.ListLatestRates(ctx, at)
	if err != nil {
		return nil, err
	}
	out := make([]app.Rate, 0, len(rows))
	for _, r := range rows {
		out = append(out, app.Rate{Pair: r.Pair, BuyMicros: r.BuyMicros, SellMicros: r.SellMicros, ValidFrom: r.ValidFrom})
	}
	return out, nil
}

func (s *Store) GetLimit(ctx context.Context, tier, currency string) (int64, int64, error) {
	row, err := s.queries.GetLimit(ctx, db.GetLimitParams{Tier: tier, Currency: currency})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, 0, app.ErrNotFound
		}
		return 0, 0, err
	}
	return row.PerTx, row.Daily, nil
}

func (s *Store) SumDailyOutgoing(ctx context.Context, customerID, currency string, since time.Time) (int64, error) {
	return s.queries.SumDailyOutgoing(ctx, db.SumDailyOutgoingParams{
		CustomerID: customerID, Currency: currency, CreatedAt: since,
	})
}

func (s *Store) ClaimStuck(ctx context.Context, states []string, staleAfter time.Duration, limit int32) ([]string, error) {
	var ids []string
	err := s.tx.WithTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		ids, err = s.queries.WithTx(tx).ClaimStuck(ctx, db.ClaimStuckParams{
			States:     states,
			StaleAfter: pgtype.Interval{Microseconds: staleAfter.Microseconds(), Valid: true},
			ClaimLimit: limit,
		})
		return err
	})
	return ids, err
}

func (s *Store) DeleteExpiredIdempotencyKeys(ctx context.Context, before time.Time) (int64, error) {
	return s.queries.DeleteExpiredIdempotencyKeys(ctx, before)
}

type storeTx struct {
	q   *db.Queries
	raw pgx.Tx
}

func (t *storeTx) TryInsertIdempotencyKey(ctx context.Context, customerID, key, transferID, requestHash string) (bool, error) {
	n, err := t.q.TryInsertIdempotencyKey(ctx, db.TryInsertIdempotencyKeyParams{
		CustomerID: customerID, Key: key, TransferID: transferID, RequestHash: requestHash,
	})
	return n == 1, err
}

func (t *storeTx) GetIdempotencyKey(ctx context.Context, customerID, key string) (string, string, error) {
	row, err := t.q.GetIdempotencyKey(ctx, db.GetIdempotencyKeyParams{CustomerID: customerID, Key: key})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", "", app.ErrNotFound
		}
		return "", "", err
	}
	return row.TransferID, row.RequestHash, nil
}

func (t *storeTx) InsertTransfer(ctx context.Context, tr *app.Transfer) error {
	var number *string
	if tr.ToAccountNumber != "" {
		number = &tr.ToAccountNumber
	}
	return t.q.InsertTransfer(ctx, db.InsertTransferParams{
		ID:              tr.ID,
		Type:            string(tr.Type),
		State:           string(tr.State),
		CustomerID:      tr.CustomerID,
		FromAccountID:   nilIfEmpty(tr.FromAccountID),
		ToAccountID:     nilIfEmpty(tr.ToAccountID),
		ToAccountNumber: number,
		Amount:          tr.Amount,
		Currency:        tr.Currency,
	})
}

func (t *storeTx) GetTransfer(ctx context.Context, id string) (*app.Transfer, error) {
	row, err := t.q.GetTransfer(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, app.ErrNotFound
		}
		return nil, err
	}
	return transferFromRow(row), nil
}

func (t *storeTx) UpdateTransferGuarded(ctx context.Context, tr *app.Transfer, expectedState domain.State) error {
	n, err := t.q.UpdateTransferGuarded(ctx, db.UpdateTransferGuardedParams{
		ID:                tr.ID,
		State:             string(tr.State),
		ToAccountID:       nilIfEmpty(tr.ToAccountID),
		CounterAmount:     tr.CounterAmount,
		CounterCurrency:   tr.CounterCurrency,
		AppliedRateMicros: tr.RateMicros,
		RatePair:          tr.RatePair,
		Reason:            tr.Reason,
		HoldID:            tr.HoldID,
		ExpectedState:     string(expectedState),
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return app.ErrStateRaced
	}
	return nil
}

func (t *storeTx) AppendEvent(ctx context.Context, transferID string, from, to domain.State, detail map[string]any) error {
	var raw []byte
	if detail != nil {
		var err error
		raw, err = json.Marshal(detail)
		if err != nil {
			return err
		}
	}
	return t.q.AppendTransferEvent(ctx, db.AppendTransferEventParams{
		TransferID: transferID,
		FromState:  string(from),
		ToState:    string(to),
		Detail:     raw,
	})
}

func (t *storeTx) InsertOutbox(ctx context.Context, m outbox.Message) error {
	return outbox.Insert(ctx, t.raw, m)
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func transferFromRow(r db.Transfer) *app.Transfer {
	t := &app.Transfer{
		ID:               r.ID,
		Type:             domain.Type(r.Type),
		State:            domain.State(r.State),
		CustomerID:       r.CustomerID,
		Amount:           r.Amount,
		Currency:         r.Currency,
		CounterAmount:    r.CounterAmount,
		CounterCurrency:  r.CounterCurrency,
		RateMicros:       r.AppliedRateMicros,
		RatePair:         r.RatePair,
		Reason:           r.Reason,
		HoldID:           r.HoldID,
		RecoveryAttempts: r.RecoveryAttempts,
		CreatedAt:        r.CreatedAt,
		UpdatedAt:        r.UpdatedAt,
	}
	if r.FromAccountID != nil {
		t.FromAccountID = *r.FromAccountID
	}
	if r.ToAccountID != nil {
		t.ToAccountID = *r.ToAccountID
	}
	if r.ToAccountNumber != nil {
		t.ToAccountNumber = *r.ToAccountNumber
	}
	return t
}
