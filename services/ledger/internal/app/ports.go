package app

import (
	"context"
	"errors"
	"time"

	"github.com/aidostt/bank-core/pkg/outbox"

	"github.com/aidostt/bank-core/services/ledger/internal/domain"
)

// Store-level sentinels the postgres adapter translates pgx errors into.
var (
	ErrNotFound = errors.New("not found")
	// ErrRefConflict: unique violation on an idempotency reference — the
	// caller re-reads and compares payloads.
	ErrRefConflict = errors.New("reference conflict")
)

// AccountRef addresses a ledger account by exactly one of its identities.
type AccountRef struct {
	ExternalID   string
	InternalCode string
	LedgerID     string
}

type PostingSpec struct {
	Ref      AccountRef
	Amount   int64
	Currency string
}

type PostingView struct {
	ID                string
	EntryID           string
	AccountID         string
	ExternalAccountID string
	Amount            int64
	Currency          string
	OccurredAt        time.Time
}

type EntryView struct {
	ID            string
	ReferenceType string
	ReferenceID   string
	OccurredAt    time.Time
	Postings      []PostingView
}

type BalanceRow struct {
	AccountID    string
	ExternalID   string
	InternalCode string
	Currency     string
	Balance      int64
	Held         int64
	Version      int64
	AsOf         time.Time
}

// Store is the app-side persistence port (ADR-0014): the domain stays pure,
// the app orchestrates, adapters implement.
type Store interface {
	WithinTx(ctx context.Context, fn func(ctx context.Context, tx StoreTx) error) error
	GetAccountByRef(ctx context.Context, ref AccountRef) (domain.Account, error)
	GetEntryRef(ctx context.Context, refType, refID string) (entryID string, occurredAt time.Time, err error)
	GetEntryWithPostings(ctx context.Context, entryID string, occurredAt time.Time) (*EntryView, error)
	GetBalances(ctx context.Context, accountIDs []string) ([]BalanceRow, error)
	ListPostings(ctx context.Context, accountID string, from, to time.Time, limit int32, cursorTime time.Time, cursorID string) ([]PostingView, error)
	ListExpiredHolds(ctx context.Context, limit int32) ([]domain.Hold, error)
}

// StoreTx is the transactional surface. Lock discipline (ADR-0007): holds
// are locked before balances; balances are locked in ascending account id —
// LockBalance callers must iterate a sorted id list.
type StoreTx interface {
	GetAccountByRef(ctx context.Context, ref AccountRef) (domain.Account, error)
	// UpsertCustomerAccount creates the account + zero balance row or
	// returns the existing one (idempotent by external id).
	UpsertCustomerAccount(ctx context.Context, externalID, currency string) (domain.Account, error)

	GetEntryRef(ctx context.Context, refType, refID string) (entryID string, occurredAt time.Time, err error)
	GetEntryWithPostings(ctx context.Context, entryID string, occurredAt time.Time) (*EntryView, error)
	InsertEntry(ctx context.Context, id, refType, refID string, occurredAt time.Time) error
	InsertEntryRef(ctx context.Context, refType, refID, entryID string, occurredAt time.Time) error
	InsertPosting(ctx context.Context, p PostingView) error

	LockBalance(ctx context.Context, accountID string) (domain.Balance, error)
	UpdateBalance(ctx context.Context, b domain.Balance) error

	GetHoldByReference(ctx context.Context, refType, refID string) (domain.Hold, error)
	LockHoldByID(ctx context.Context, id string) (domain.Hold, error)
	InsertHold(ctx context.Context, h domain.Hold) error
	SetHoldStatus(ctx context.Context, id, status string) error

	InsertOutbox(ctx context.Context, m outbox.Message) error
}
