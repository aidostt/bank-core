// Package app implements account use cases. Registry writes are strongly
// consistent; balances are read through the ledger for M1 (the projection
// consumer ships in M2 — see docs/services/account-service.md, M1 notes).
package app

import (
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"math/big"
	"time"

	accountv1 "github.com/aidostt/bank-core/gen/go/bank/account/v1"
	eventsv1 "github.com/aidostt/bank-core/gen/go/bank/events/v1"
	"github.com/aidostt/bank-core/pkg/apperr"
	"github.com/aidostt/bank-core/pkg/grpcx"
	"github.com/aidostt/bank-core/pkg/logging"
	"github.com/aidostt/bank-core/pkg/money"
	"github.com/aidostt/bank-core/pkg/outbox"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/aidostt/bank-core/services/account/internal/adapters/postgres"
	"github.com/aidostt/bank-core/services/account/internal/adapters/postgres/db"
	"github.com/aidostt/bank-core/services/account/internal/domain"
)

// LedgerClient is the app-side port to the ledger (ADR-0014).
type LedgerClient interface {
	// CreateAccount creates the ledger mirror; idempotent by external id.
	CreateAccount(ctx context.Context, externalAccountID, currency string) error
	GetBalances(ctx context.Context, externalAccountIDs []string) (map[string]BalanceView, error)
	ListPostings(ctx context.Context, externalAccountID string, from, to time.Time, pageSize int32, cursor string) ([]TransactionView, string, error)
}

type BalanceView struct {
	AccountID string
	Balance   int64
	Held      int64
	Available int64
	AsOf      time.Time
}

type TransactionView struct {
	EntryID    string
	AccountID  string
	Amount     int64
	Currency   string
	OccurredAt time.Time
}

type AccountView struct {
	ID         string
	CustomerID string
	UserID     string
	Number     string
	Currency   string
	Status     string
	OpenedAt   time.Time
}

type Service struct {
	store  *postgres.Store
	ledger LedgerClient
	log    *slog.Logger
}

func NewService(store *postgres.Store, ledger LedgerClient, log *slog.Logger) *Service {
	return &Service{store: store, ledger: ledger, log: log}
}

// OpenAccount: number with check digit, ledger mirror created synchronously
// BEFORE the row is visible — money can never move on an unmirrored account.
// The customer row is bootstrapped lazily in M1 (customers.registered
// consumer lands in M2).
func (s *Service) OpenAccount(ctx context.Context, caller grpcx.Claims, currency string) (*AccountView, error) {
	if caller.CustomerID == "" {
		return nil, apperr.New(apperr.CodeUnauthenticated, "no caller identity")
	}
	if !money.ValidCurrency(currency) {
		return nil, apperr.Newf(apperr.CodeInvalidArgument, "unsupported currency %q", currency)
	}
	random, err := randomDigits(13)
	if err != nil {
		return nil, apperr.Wrap(apperr.CodeInternal, "entropy", err)
	}
	number, err := domain.GenerateNumber(random)
	if err != nil {
		return nil, apperr.Wrap(apperr.CodeInternal, "generate number", err)
	}
	id, err := uuid.NewV7()
	if err != nil {
		return nil, apperr.Wrap(apperr.CodeInternal, "uuid", err)
	}

	customer, err := s.store.Queries().UpsertCustomer(ctx, caller.CustomerID)
	if err != nil {
		return nil, err
	}
	// Sync mirror first (idempotent by external id): if this fails the
	// account is not created at all.
	if err := s.ledger.CreateAccount(ctx, id.String(), currency); err != nil {
		return nil, err
	}
	var view *AccountView
	err = s.store.WithTx(ctx, func(ctx context.Context, q *db.Queries, tx pgx.Tx) error {
		acc, err := q.CreateAccount(ctx, db.CreateAccountParams{
			ID:         id.String(),
			CustomerID: customer.ID,
			Number:     number,
			Currency:   currency,
		})
		if err != nil {
			return err
		}
		view = &AccountView{
			ID: acc.ID, CustomerID: acc.CustomerID, UserID: caller.CustomerID,
			Number: acc.Number, Currency: acc.Currency, Status: acc.Status, OpenedAt: acc.OpenedAt,
		}
		// AccountOpened rides the outbox in the same tx (ADR-0009).
		msg, err := outbox.NewProtoMessage(ctx, "accounts.events", acc.ID,
			logging.RequestID(ctx), &eventsv1.AccountOpened{Account: &accountv1.Account{
				Id: acc.ID, CustomerId: acc.CustomerID, UserId: caller.CustomerID,
				Number: acc.Number, Currency: acc.Currency, Status: acc.Status,
				OpenedAt: timestamppb.New(acc.OpenedAt),
			}})
		if err != nil {
			return err
		}
		return outbox.Insert(ctx, tx, msg)
	})
	if err != nil {
		return nil, err
	}
	return view, nil
}

func (s *Service) GetAccount(ctx context.Context, caller grpcx.Claims, accountID string) (*AccountView, error) {
	row, err := s.store.Queries().GetAccountByID(ctx, accountID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperr.New(apperr.CodeNotFound, "account not found")
		}
		return nil, err
	}
	view := rowToView(row.ID, row.CustomerID, row.UserID, row.Number, row.Currency, row.Status, row.OpenedAt)
	// Ownership is asserted for customer-role callers (architecture §6);
	// staff may read any account.
	if !caller.IsStaff() && row.UserID != caller.CustomerID {
		return nil, apperr.New(apperr.CodeForbidden, "account does not belong to caller")
	}
	return view, nil
}

// ResolveByNumber returns any account by number — the P2P destination
// lookup. Limited use: no balance data is exposed here.
func (s *Service) ResolveByNumber(ctx context.Context, number string) (*AccountView, error) {
	if !domain.ValidNumber(number) {
		return nil, apperr.New(apperr.CodeInvalidArgument, "malformed account number")
	}
	row, err := s.store.Queries().GetAccountByNumber(ctx, number)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperr.New(apperr.CodeNotFound, "account not found")
		}
		return nil, err
	}
	return rowToView(row.ID, row.CustomerID, row.UserID, row.Number, row.Currency, row.Status, row.OpenedAt), nil
}

type AccountWithBalance struct {
	Account AccountView
	Balance *BalanceView
}

// ListAccounts returns the caller's accounts (or userID's for staff) with
// balances from the local projection (M2): eventually consistent, staleness
// exposed honestly via as_of (ADR-0006). Holds are not projected — available
// equals the settled balance (account doc note).
func (s *Service) ListAccounts(ctx context.Context, caller grpcx.Claims, userID string) ([]AccountWithBalance, error) {
	target := caller.CustomerID
	if userID != "" && userID != caller.CustomerID {
		if !caller.IsStaff() {
			return nil, apperr.New(apperr.CodeForbidden, "cannot list another customer's accounts")
		}
		target = userID
	}
	if target == "" {
		return nil, apperr.New(apperr.CodeUnauthenticated, "no caller identity")
	}
	rows, err := s.store.Queries().ListAccountsByUser(ctx, target)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	balances := map[string]BalanceView{}
	if len(ids) > 0 {
		projected, err := s.store.Queries().GetBalancesForAccounts(ctx, ids)
		if err != nil {
			return nil, err
		}
		for _, b := range projected {
			balances[b.AccountID] = BalanceView{
				AccountID: b.AccountID, Balance: b.Balance,
				Available: b.Balance, AsOf: b.AsOf,
			}
		}
	}
	out := make([]AccountWithBalance, 0, len(rows))
	for _, r := range rows {
		item := AccountWithBalance{
			Account: *rowToView(r.ID, r.CustomerID, r.UserID, r.Number, r.Currency, r.Status, r.OpenedAt),
		}
		if b, ok := balances[r.ID]; ok {
			item.Balance = &b
		} else {
			// No postings consumed yet — a zero balance with a zero as_of is
			// honest for a freshly opened account.
			item.Balance = &BalanceView{AccountID: r.ID}
		}
		out = append(out, item)
	}
	return out, nil
}

func (s *Service) setStatus(ctx context.Context, caller grpcx.Claims, accountID, to string) (*AccountView, error) {
	if !caller.IsStaff() {
		return nil, apperr.New(apperr.CodeForbidden, "support/admin role required")
	}
	row, err := s.store.Queries().GetAccountByID(ctx, accountID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperr.New(apperr.CodeNotFound, "account not found")
		}
		return nil, err
	}
	if row.Status == to {
		return rowToView(row.ID, row.CustomerID, row.UserID, row.Number, row.Currency, row.Status, row.OpenedAt), nil
	}
	if !domain.CanTransition(row.Status, to) {
		return nil, apperr.Newf(apperr.CodeInvalidArgument, "cannot transition %s → %s", row.Status, to)
	}
	acc, err := s.store.Queries().UpdateAccountStatus(ctx, db.UpdateAccountStatusParams{ID: accountID, Status: to})
	if err != nil {
		return nil, err
	}
	return rowToView(acc.ID, acc.CustomerID, row.UserID, acc.Number, acc.Currency, acc.Status, acc.OpenedAt), nil
}

func (s *Service) Freeze(ctx context.Context, caller grpcx.Claims, accountID, reason string) (*AccountView, error) {
	view, err := s.setStatus(ctx, caller, accountID, domain.StatusFrozen)
	if err == nil {
		s.log.InfoContext(ctx, "account frozen",
			slog.String("account.id", accountID), slog.String("reason", reason))
	}
	return view, err
}

func (s *Service) Unfreeze(ctx context.Context, caller grpcx.Claims, accountID string) (*AccountView, error) {
	return s.setStatus(ctx, caller, accountID, domain.StatusActive)
}

func (s *Service) GetBalances(ctx context.Context, caller grpcx.Claims, accountIDs []string) ([]BalanceView, error) {
	if len(accountIDs) == 0 {
		return nil, apperr.New(apperr.CodeInvalidArgument, "account_ids required")
	}
	for _, id := range accountIDs {
		if _, err := s.GetAccount(ctx, caller, id); err != nil {
			return nil, err
		}
	}
	m, err := s.ledger.GetBalances(ctx, accountIDs)
	if err != nil {
		return nil, err
	}
	out := make([]BalanceView, 0, len(m))
	for _, id := range accountIDs {
		if b, ok := m[id]; ok {
			out = append(out, b)
		}
	}
	return out, nil
}

func (s *Service) ListTransactions(ctx context.Context, caller grpcx.Claims, accountID string, from, to time.Time, pageSize int32, cursor string) ([]TransactionView, string, error) {
	if _, err := s.GetAccount(ctx, caller, accountID); err != nil {
		return nil, "", err
	}
	return s.ledger.ListPostings(ctx, accountID, from, to, pageSize, cursor)
}

func rowToView(id, customerID, userID, number, currency, status string, openedAt time.Time) *AccountView {
	return &AccountView{ID: id, CustomerID: customerID, UserID: userID,
		Number: number, Currency: currency, Status: status, OpenedAt: openedAt}
}

func randomDigits(n int) (string, error) {
	max := big.NewInt(10)
	out := make([]byte, n)
	for i := range out {
		d, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		out[i] = byte('0' + d.Int64()) // #nosec G115 -- d ∈ [0,9] by rand.Int bound
	}
	return string(out), nil
}
