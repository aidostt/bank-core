package app

import (
	"context"
	"errors"
	"time"

	"github.com/aidostt/bank-core/pkg/outbox"

	"github.com/aidostt/bank-core/services/transfer/internal/domain"
)

var (
	ErrNotFound = errors.New("not found")
	// ErrStateRaced: optimistic state-guarded update hit 0 rows — another
	// worker moved the transfer; reload and continue.
	ErrStateRaced = errors.New("transfer state raced")
)

// Transfer is the app view of a transfer row.
type Transfer struct {
	ID               string
	Type             domain.Type
	State            domain.State
	CustomerID       string
	FromAccountID    string
	ToAccountID      string
	ToAccountNumber  string
	Amount           int64
	Currency         string
	CounterAmount    *int64
	CounterCurrency  *string
	RateMicros       *int64
	RatePair         *string
	Reason           *string
	HoldID           *string
	RecoveryAttempts int32
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

type Rate struct {
	Pair       string
	BuyMicros  int64
	SellMicros int64
	ValidFrom  time.Time
}

// LedgerAccountRef mirrors the ledger AccountRef oneof.
type LedgerAccountRef struct {
	ExternalID   string
	InternalCode string
}

type LedgerPosting struct {
	Ref      LedgerAccountRef
	Amount   int64
	Currency string
}

// LedgerClient is the saga's port to the ledger. Every method is idempotent
// by transfer id (ADR-0010) — the recovery worker relies on this.
type LedgerClient interface {
	PlaceHold(ctx context.Context, ref LedgerAccountRef, amount int64, currency, refID string) (holdID string, err error)
	ReleaseHold(ctx context.Context, holdID string) error
	PostTransaction(ctx context.Context, refID, holdID string, postings []LedgerPosting) error
	// TransactionExists reports whether an entry with this reference was
	// ever posted — the recovery worker's disambiguation probe.
	TransactionExists(ctx context.Context, refID string) (bool, error)
}

type AccountInfo struct {
	ID       string
	UserID   string
	Number   string
	Currency string
	Status   string
}

type AccountClient interface {
	GetAccount(ctx context.Context, accountID string) (AccountInfo, error)
	ResolveByNumber(ctx context.Context, number string) (AccountInfo, error)
}

// Store is the app-side persistence port.
type Store interface {
	WithinTx(ctx context.Context, fn func(ctx context.Context, tx StoreTx) error) error
	GetTransfer(ctx context.Context, id string) (*Transfer, error)
	ListTransfersByCustomer(ctx context.Context, customerID string, limit int32, cursorCreatedAt time.Time, cursorID string) ([]*Transfer, error)
	GetLatestRate(ctx context.Context, pair string, at time.Time) (Rate, error)
	ListLatestRates(ctx context.Context, at time.Time) ([]Rate, error)
	GetLimit(ctx context.Context, tier, currency string) (perTx, daily int64, err error)
	SumDailyOutgoing(ctx context.Context, customerID, currency string, since time.Time) (int64, error)
	// ClaimStuck selects non-terminal stale transfers with FOR UPDATE SKIP
	// LOCKED, bumps recovery_attempts + updated_at, and returns their ids.
	ClaimStuck(ctx context.Context, states []string, staleAfter time.Duration, limit int32) ([]string, error)
	DeleteExpiredIdempotencyKeys(ctx context.Context, before time.Time) (int64, error)
}

type StoreTx interface {
	// TryInsertIdempotencyKey returns false on (customer_id, key) conflict.
	TryInsertIdempotencyKey(ctx context.Context, customerID, key, transferID, requestHash string) (bool, error)
	GetIdempotencyKey(ctx context.Context, customerID, key string) (transferID, requestHash string, err error)
	InsertTransfer(ctx context.Context, t *Transfer) error
	GetTransfer(ctx context.Context, id string) (*Transfer, error)
	// UpdateTransferGuarded persists t only if the row is still in
	// expectedState; returns ErrStateRaced on 0 rows.
	UpdateTransferGuarded(ctx context.Context, t *Transfer, expectedState domain.State) error
	AppendEvent(ctx context.Context, transferID string, from, to domain.State, detail map[string]any) error
	InsertOutbox(ctx context.Context, m outbox.Message) error
}
