// Package postgres implements the app Store/StoreTx ports (ADR-0005,
// ADR-0007). Lock ordering lives in the app layer; this adapter only
// executes single-row locks.
package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/aidostt/bank-core/pkg/outbox"
	"github.com/aidostt/bank-core/pkg/pgtx"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aidostt/bank-core/services/ledger/internal/adapters/postgres/db"
	"github.com/aidostt/bank-core/services/ledger/internal/app"
	"github.com/aidostt/bank-core/services/ledger/internal/domain"
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

func (s *Store) GetAccountByRef(ctx context.Context, ref app.AccountRef) (domain.Account, error) {
	return getAccountByRef(ctx, s.queries, ref)
}

func (s *Store) GetEntryRef(ctx context.Context, refType, refID string) (string, time.Time, error) {
	return getEntryRef(ctx, s.queries, refType, refID)
}

func (s *Store) GetEntryWithPostings(ctx context.Context, entryID string, occurredAt time.Time) (*app.EntryView, error) {
	return getEntryWithPostings(ctx, s.queries, entryID, occurredAt)
}

func (s *Store) GetBalances(ctx context.Context, accountIDs []string) ([]app.BalanceRow, error) {
	rows, err := s.queries.GetBalancesByIDs(ctx, accountIDs)
	if err != nil {
		return nil, err
	}
	out := make([]app.BalanceRow, 0, len(rows))
	for _, r := range rows {
		row := app.BalanceRow{
			AccountID: r.AccountID, Currency: r.Currency,
			Balance: r.Balance, Held: r.Held, Version: r.Version, AsOf: r.UpdatedAt,
		}
		if r.ExternalAccountID != nil {
			row.ExternalID = *r.ExternalAccountID
		}
		if r.InternalCode != nil {
			row.InternalCode = *r.InternalCode
		}
		out = append(out, row)
	}
	return out, nil
}

func (s *Store) ListPostings(ctx context.Context, accountID string, from, to time.Time, limit int32, cursorTime time.Time, cursorID string) ([]app.PostingView, error) {
	rows, err := s.queries.ListPostingsPage(ctx, db.ListPostingsPageParams{
		AccountID:  accountID,
		FromTime:   from,
		ToTime:     to,
		CursorTime: cursorTime,
		CursorID:   cursorID,
		PageLimit:  limit,
	})
	if err != nil {
		return nil, err
	}
	out := make([]app.PostingView, 0, len(rows))
	for _, r := range rows {
		pv := app.PostingView{
			ID: r.ID, EntryID: r.EntryID, AccountID: r.AccountID,
			Amount: r.Amount, Currency: r.Currency, OccurredAt: r.OccurredAt,
		}
		if r.ExternalAccountID != nil {
			pv.ExternalAccountID = *r.ExternalAccountID
		}
		out = append(out, pv)
	}
	return out, nil
}

func (s *Store) ListExpiredHolds(ctx context.Context, limit int32) ([]domain.Hold, error) {
	rows, err := s.queries.ListExpiredHolds(ctx, limit)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Hold, 0, len(rows))
	for _, r := range rows {
		out = append(out, holdFromRow(r))
	}
	return out, nil
}

// SumPostingsForAccount supports tests and verify-ledger (invariant 4).
func (s *Store) SumPostingsForAccount(ctx context.Context, accountID string) (int64, error) {
	return s.queries.SumPostingsForAccount(ctx, accountID)
}

type storeTx struct {
	q   *db.Queries
	raw pgx.Tx
}

func (t *storeTx) GetAccountByRef(ctx context.Context, ref app.AccountRef) (domain.Account, error) {
	return getAccountByRef(ctx, t.q, ref)
}

func (t *storeTx) UpsertCustomerAccount(ctx context.Context, externalID, currency string) (domain.Account, error) {
	row, err := t.q.InsertCustomerAccount(ctx, db.InsertCustomerAccountParams{
		ExternalAccountID: &externalID,
		Currency:          currency,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// Conflict: already exists — return it (idempotent).
		existing, err := t.q.GetAccountByExternalID(ctx, &externalID)
		if err != nil {
			return domain.Account{}, mapErr(err)
		}
		return accountFromRow(existing), nil
	}
	if err != nil {
		return domain.Account{}, mapErr(err)
	}
	if err := t.q.InsertBalanceRow(ctx, db.InsertBalanceRowParams{AccountID: row.ID, Currency: currency}); err != nil {
		return domain.Account{}, err
	}
	return accountFromRow(row), nil
}

func (t *storeTx) GetEntryRef(ctx context.Context, refType, refID string) (string, time.Time, error) {
	return getEntryRef(ctx, t.q, refType, refID)
}

func (t *storeTx) GetEntryWithPostings(ctx context.Context, entryID string, occurredAt time.Time) (*app.EntryView, error) {
	return getEntryWithPostings(ctx, t.q, entryID, occurredAt)
}

func (t *storeTx) InsertEntry(ctx context.Context, id, refType, refID string, occurredAt time.Time) error {
	return mapErr(t.q.InsertEntry(ctx, db.InsertEntryParams{
		ID: id, ReferenceType: refType, ReferenceID: refID, OccurredAt: occurredAt,
	}))
}

func (t *storeTx) InsertEntryRef(ctx context.Context, refType, refID, entryID string, occurredAt time.Time) error {
	return mapErr(t.q.InsertEntryRef(ctx, db.InsertEntryRefParams{
		ReferenceType: refType, ReferenceID: refID, EntryID: entryID, OccurredAt: occurredAt,
	}))
}

func (t *storeTx) InsertPosting(ctx context.Context, p app.PostingView) error {
	return t.q.InsertPosting(ctx, db.InsertPostingParams{
		ID: p.ID, EntryID: p.EntryID, AccountID: p.AccountID,
		Amount: p.Amount, Currency: p.Currency, OccurredAt: p.OccurredAt,
	})
}

func (t *storeTx) LockBalance(ctx context.Context, accountID string) (domain.Balance, error) {
	row, err := t.q.LockBalance(ctx, accountID)
	if err != nil {
		return domain.Balance{}, mapErr(err)
	}
	return domain.Balance{
		AccountID: row.AccountID, Currency: row.Currency,
		Balance: row.Balance, Held: row.Held, Version: row.Version,
	}, nil
}

func (t *storeTx) UpdateBalance(ctx context.Context, b domain.Balance) error {
	return t.q.UpdateBalance(ctx, db.UpdateBalanceParams{
		AccountID: b.AccountID, Balance: b.Balance, Held: b.Held, Version: b.Version,
	})
}

func (t *storeTx) GetHoldByReference(ctx context.Context, refType, refID string) (domain.Hold, error) {
	row, err := t.q.GetHoldByReference(ctx, db.GetHoldByReferenceParams{ReferenceType: refType, ReferenceID: refID})
	if err != nil {
		return domain.Hold{}, mapErr(err)
	}
	return holdFromRow(row), nil
}

func (t *storeTx) LockHoldByID(ctx context.Context, id string) (domain.Hold, error) {
	row, err := t.q.LockHoldByID(ctx, id)
	if err != nil {
		return domain.Hold{}, mapErr(err)
	}
	return holdFromRow(row), nil
}

func (t *storeTx) InsertHold(ctx context.Context, h domain.Hold) error {
	return mapErr(t.q.InsertHold(ctx, db.InsertHoldParams{
		ID: h.ID, AccountID: h.AccountID, Amount: h.Amount, Currency: h.Currency,
		ReferenceType: h.ReferenceType, ReferenceID: h.ReferenceID,
		Status: h.Status, ExpiresAt: h.ExpiresAt,
	}))
}

func (t *storeTx) SetHoldStatus(ctx context.Context, id, status string) error {
	return t.q.SetHoldStatus(ctx, db.SetHoldStatusParams{ID: id, Status: status})
}

func (t *storeTx) InsertOutbox(ctx context.Context, m outbox.Message) error {
	return outbox.Insert(ctx, t.raw, m)
}

// --- shared helpers ---

type accountQuerier interface {
	GetAccountByID(ctx context.Context, id string) (db.LedgerAccount, error)
	GetAccountByExternalID(ctx context.Context, externalAccountID *string) (db.LedgerAccount, error)
	GetAccountByInternalCode(ctx context.Context, internalCode *string) (db.LedgerAccount, error)
}

func getAccountByRef(ctx context.Context, q accountQuerier, ref app.AccountRef) (domain.Account, error) {
	var row db.LedgerAccount
	var err error
	switch {
	case ref.ExternalID != "":
		row, err = q.GetAccountByExternalID(ctx, &ref.ExternalID)
	case ref.InternalCode != "":
		row, err = q.GetAccountByInternalCode(ctx, &ref.InternalCode)
	case ref.LedgerID != "":
		row, err = q.GetAccountByID(ctx, ref.LedgerID)
	default:
		return domain.Account{}, domain.ErrAccountNotFound
	}
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Account{}, domain.ErrAccountNotFound
		}
		return domain.Account{}, err
	}
	return accountFromRow(row), nil
}

type entryQuerier interface {
	GetEntryRef(ctx context.Context, arg db.GetEntryRefParams) (db.JournalEntryRef, error)
	GetEntry(ctx context.Context, arg db.GetEntryParams) (db.JournalEntry, error)
	GetEntryPostings(ctx context.Context, arg db.GetEntryPostingsParams) ([]db.GetEntryPostingsRow, error)
}

func getEntryRef(ctx context.Context, q entryQuerier, refType, refID string) (string, time.Time, error) {
	ref, err := q.GetEntryRef(ctx, db.GetEntryRefParams{ReferenceType: refType, ReferenceID: refID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", time.Time{}, app.ErrNotFound
		}
		return "", time.Time{}, err
	}
	return ref.EntryID, ref.OccurredAt, nil
}

func getEntryWithPostings(ctx context.Context, q entryQuerier, entryID string, occurredAt time.Time) (*app.EntryView, error) {
	entry, err := q.GetEntry(ctx, db.GetEntryParams{ID: entryID, OccurredAt: occurredAt})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, app.ErrNotFound
		}
		return nil, err
	}
	postings, err := q.GetEntryPostings(ctx, db.GetEntryPostingsParams{EntryID: entryID, OccurredAt: occurredAt})
	if err != nil {
		return nil, err
	}
	view := &app.EntryView{
		ID: entry.ID, ReferenceType: entry.ReferenceType, ReferenceID: entry.ReferenceID,
		OccurredAt: entry.OccurredAt,
	}
	for _, p := range postings {
		pv := app.PostingView{
			ID: p.ID, EntryID: p.EntryID, AccountID: p.AccountID,
			Amount: p.Amount, Currency: p.Currency, OccurredAt: p.OccurredAt,
		}
		if p.ExternalAccountID != nil {
			pv.ExternalAccountID = *p.ExternalAccountID
		}
		view.Postings = append(view.Postings, pv)
	}
	return view, nil
}

func accountFromRow(r db.LedgerAccount) domain.Account {
	a := domain.Account{ID: r.ID, Type: r.Type, Currency: r.Currency, Status: r.Status}
	if r.ExternalAccountID != nil {
		a.ExternalID = *r.ExternalAccountID
	}
	if r.InternalCode != nil {
		a.InternalCode = *r.InternalCode
	}
	return a
}

func holdFromRow(r db.Hold) domain.Hold {
	return domain.Hold{
		ID: r.ID, AccountID: r.AccountID, Amount: r.Amount, Currency: r.Currency,
		ReferenceType: r.ReferenceType, ReferenceID: r.ReferenceID,
		Status: r.Status, ExpiresAt: r.ExpiresAt,
	}
}

// mapErr converts unique violations into the app conflict sentinel so the
// service can resolve idempotency races.
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return app.ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return app.ErrRefConflict
	}
	return err
}
