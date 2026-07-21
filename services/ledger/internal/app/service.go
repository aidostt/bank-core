// Package app orchestrates the ledger use cases around the pure domain
// (docs/services/ledger-service.md "Posting algorithm").
package app

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	commonv1 "github.com/aidostt/bank-core/gen/go/bank/common/v1"
	eventsv1 "github.com/aidostt/bank-core/gen/go/bank/events/v1"
	ledgerv1 "github.com/aidostt/bank-core/gen/go/bank/ledger/v1"
	"github.com/aidostt/bank-core/pkg/apperr"
	"github.com/aidostt/bank-core/pkg/logging"
	"github.com/aidostt/bank-core/pkg/metrics"
	"github.com/aidostt/bank-core/pkg/money"
	"github.com/aidostt/bank-core/pkg/outbox"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/aidostt/bank-core/services/ledger/internal/domain"
)

const (
	TopicTransactions = "ledger.transactions"
	maxPageSize       = 200
	defaultPageSize   = 50
	uuidMax           = "ffffffff-ffff-ffff-ffff-ffffffffffff"
)

type Service struct {
	store          Store
	holdDefaultTTL time.Duration
	log            *slog.Logger
}

func NewService(store Store, holdDefaultTTL time.Duration, log *slog.Logger) *Service {
	if holdDefaultTTL <= 0 {
		holdDefaultTTL = 10 * time.Minute
	}
	return &Service{store: store, holdDefaultTTL: holdDefaultTTL, log: log}
}

// CreateAccount is idempotent by external_account_id.
func (s *Service) CreateAccount(ctx context.Context, externalID, currency string) (domain.Account, error) {
	if externalID == "" {
		return domain.Account{}, apperr.New(apperr.CodeInvalidArgument, "external_account_id required")
	}
	if !money.ValidCurrency(currency) {
		return domain.Account{}, apperr.Newf(apperr.CodeCurrencyMismatch, "unsupported currency %q", currency)
	}
	var acc domain.Account
	err := s.store.WithinTx(ctx, func(ctx context.Context, tx StoreTx) error {
		var err error
		acc, err = tx.UpsertCustomerAccount(ctx, externalID, currency)
		return err
	})
	if err != nil {
		return domain.Account{}, toAppErr(err)
	}
	if acc.Currency != currency {
		return domain.Account{}, apperr.Newf(apperr.CodeAlreadyExists,
			"account %s already exists with currency %s", externalID, acc.Currency)
	}
	return acc, nil
}

// PlaceHold reserves available balance; idempotent by (reference_type,
// reference_id).
func (s *Service) PlaceHold(ctx context.Context, ref AccountRef, amount int64, currency, refType, refID string, ttl time.Duration) (domain.Hold, error) {
	if refType == "" || refID == "" {
		return domain.Hold{}, apperr.New(apperr.CodeInvalidArgument, "reference required")
	}
	if ttl <= 0 {
		ttl = s.holdDefaultTTL
	}
	var hold domain.Hold
	err := s.store.WithinTx(ctx, func(ctx context.Context, tx StoreTx) error {
		acct, err := tx.GetAccountByRef(ctx, ref)
		if err != nil {
			return err
		}
		// Idempotent replay.
		if existing, err := tx.GetHoldByReference(ctx, refType, refID); err == nil {
			if existing.AccountID != acct.ID || existing.Amount != amount {
				return domain.ErrDuplicateReference
			}
			hold = existing
			return nil
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
		if currency != acct.Currency {
			return domain.ErrCurrencyMismatch
		}
		if acct.Type == domain.TypeCustomer && acct.Status != domain.StatusActive {
			return domain.ErrAccountFrozen
		}
		b, err := tx.LockBalance(ctx, acct.ID)
		if err != nil {
			return err
		}
		if err := domain.CanPlaceHold(b, acct.Type, amount); err != nil {
			return err
		}
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		hold = domain.Hold{
			ID: id.String(), AccountID: acct.ID, Amount: amount, Currency: currency,
			ReferenceType: refType, ReferenceID: refID,
			Status: domain.HoldActive, ExpiresAt: time.Now().UTC().Add(ttl),
		}
		if err := tx.InsertHold(ctx, hold); err != nil {
			return err
		}
		b.Held += amount // held changes do not bump version (invariant 5)
		return tx.UpdateBalance(ctx, b)
	})
	if err != nil {
		// Lost a race on the unique reference: someone placed it — replay.
		if errors.Is(err, ErrRefConflict) {
			return s.PlaceHold(ctx, ref, amount, currency, refType, refID, ttl)
		}
		return domain.Hold{}, toAppErr(err)
	}
	return hold, nil
}

// ReleaseHold is idempotent: releasing a released hold returns it unchanged.
func (s *Service) ReleaseHold(ctx context.Context, holdID string) (domain.Hold, error) {
	var hold domain.Hold
	err := s.store.WithinTx(ctx, func(ctx context.Context, tx StoreTx) error {
		h, err := tx.LockHoldByID(ctx, holdID)
		if err != nil {
			return err
		}
		released, changed, err := domain.ReleaseHold(h)
		if err != nil {
			return err
		}
		hold = released
		if !changed {
			return nil
		}
		b, err := tx.LockBalance(ctx, h.AccountID)
		if err != nil {
			return err
		}
		b.Held -= h.Amount
		if b.Held < 0 {
			return fmt.Errorf("held underflow on account %s", h.AccountID)
		}
		if err := tx.UpdateBalance(ctx, b); err != nil {
			return err
		}
		return tx.SetHoldStatus(ctx, h.ID, domain.HoldReleased)
	})
	if err != nil {
		return domain.Hold{}, toAppErr(err)
	}
	return hold, nil
}

// PostTransaction is the heart (ledger doc): one DB transaction that locks
// balances in canonical order, validates every invariant, appends the
// immutable entry, materializes balances and writes the outbox events.
// Idempotent by (reference_type, reference_id).
func (s *Service) PostTransaction(ctx context.Context, refType, refID, holdID string, specs []PostingSpec) (*EntryView, error) {
	if refType == "" || refID == "" {
		return nil, apperr.New(apperr.CodeInvalidArgument, "reference required")
	}
	view, err := s.postOnce(ctx, refType, refID, holdID, specs)
	if errors.Is(err, ErrRefConflict) {
		// Concurrent duplicate: the reference row now exists — replay path.
		return s.replayByReference(ctx, refType, refID, specs)
	}
	if err != nil {
		return nil, toAppErr(err)
	}
	metrics.LedgerPostingsTotal.Add(float64(len(view.Postings)))
	return view, nil
}

func (s *Service) postOnce(ctx context.Context, refType, refID, holdID string, specs []PostingSpec) (*EntryView, error) {
	var result *EntryView
	err := s.store.WithinTx(ctx, func(ctx context.Context, tx StoreTx) error {
		// 1. Resolve accounts.
		accounts := make(map[string]domain.Account, len(specs))
		postings := make([]domain.Posting, 0, len(specs))
		order := make([]string, 0, len(specs)) // account id per spec, in request order
		for _, spec := range specs {
			acct, err := tx.GetAccountByRef(ctx, spec.Ref)
			if err != nil {
				return err
			}
			accounts[acct.ID] = acct
			postings = append(postings, domain.Posting{AccountID: acct.ID, Amount: spec.Amount, Currency: spec.Currency})
			order = append(order, acct.ID)
		}

		// 2. Idempotency fast path (invariant 6).
		if entryID, occAt, err := tx.GetEntryRef(ctx, refType, refID); err == nil {
			existing, err := tx.GetEntryWithPostings(ctx, entryID, occAt)
			if err != nil {
				return err
			}
			if !samePayload(existing, postings) {
				return domain.ErrDuplicateReference
			}
			result = existing
			return nil
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}

		// 3. Pure validation (invariant 1 + structure).
		if err := domain.ValidateEntry(postings, accounts); err != nil {
			return err
		}

		// Net delta per account.
		delta := map[string]int64{}
		for _, p := range postings {
			d, ok := addInt64(delta[p.AccountID], p.Amount)
			if !ok {
				return domain.ErrOverflow
			}
			delta[p.AccountID] = d
		}

		// 4. Lock discipline: hold first, then balances ascending (ADR-0007).
		heldAdj := map[string]int64{}
		if holdID != "" {
			h, err := tx.LockHoldByID(ctx, holdID)
			if err != nil {
				if errors.Is(err, ErrNotFound) {
					return domain.ErrHoldNotActive
				}
				return err
			}
			if h.ReferenceType != refType || h.ReferenceID != refID {
				return domain.ErrHoldAccountMismatch
			}
			debit := delta[h.AccountID]
			if debit > 0 {
				debit = 0
			}
			captured, err := domain.CaptureHold(h, debit)
			if err != nil {
				return err
			}
			if err := tx.SetHoldStatus(ctx, captured.ID, captured.Status); err != nil {
				return err
			}
			heldAdj[h.AccountID] = -h.Amount
		}

		affected := make([]string, 0, len(delta))
		for id := range delta {
			affected = append(affected, id)
		}
		sort.Strings(affected)

		balances := make(map[string]domain.Balance, len(affected))
		for _, id := range affected {
			b, err := tx.LockBalance(ctx, id)
			if err != nil {
				return err
			}
			b.Held += heldAdj[id]
			if b.Held < 0 {
				return fmt.Errorf("held underflow on account %s", id)
			}
			// Invariants 3 + 5 via the pure function.
			nb, err := domain.ApplyPosting(b, accounts[id].Type, delta[id])
			if err != nil {
				return err
			}
			balances[id] = nb
		}

		// 5. Append the immutable entry (invariant 2: no updates, ever).
		occurredAt := time.Now().UTC().Truncate(time.Microsecond)
		entryUUID, err := uuid.NewV7()
		if err != nil {
			return err
		}
		entryID := entryUUID.String()
		if err := tx.InsertEntry(ctx, entryID, refType, refID, occurredAt); err != nil {
			return err
		}
		if err := tx.InsertEntryRef(ctx, refType, refID, entryID, occurredAt); err != nil {
			return err
		}
		view := &EntryView{ID: entryID, ReferenceType: refType, ReferenceID: refID, OccurredAt: occurredAt}
		for i, p := range postings {
			pid, err := uuid.NewV7()
			if err != nil {
				return err
			}
			pv := PostingView{
				ID: pid.String(), EntryID: entryID, AccountID: p.AccountID,
				ExternalAccountID: accounts[order[i]].ExternalID,
				Amount:            p.Amount, Currency: p.Currency, OccurredAt: occurredAt,
			}
			if err := tx.InsertPosting(ctx, pv); err != nil {
				return err
			}
			view.Postings = append(view.Postings, pv)
		}

		// 6. Materialize balances atomically with the postings (ADR-0006).
		for _, id := range affected {
			if err := tx.UpdateBalance(ctx, balances[id]); err != nil {
				return err
			}
		}

		// 7. Outbox: one event per affected account, key = account id
		// (per-account ordering for projections — architecture §5).
		protoEntry := entryToProto(view)
		requestID := logging.RequestID(ctx)
		for _, id := range affected {
			acct := accounts[id]
			key := acct.ExternalID
			if key == "" {
				key = acct.InternalCode
			}
			event := &eventsv1.TransactionPosted{
				Entry:             protoEntry,
				AccountId:         id,
				ExternalAccountId: acct.ExternalID,
				Currency:          acct.Currency,
				BalanceAfter:      balances[id].Balance,
				Version:           balances[id].Version,
			}
			msg, err := outbox.NewProtoMessage(ctx, TopicTransactions, key, requestID, event)
			if err != nil {
				return err
			}
			if err := tx.InsertOutbox(ctx, msg); err != nil {
				return err
			}
		}
		result = view
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// replayByReference resolves a lost idempotency race after commit-time
// conflict on journal_entry_refs.
func (s *Service) replayByReference(ctx context.Context, refType, refID string, specs []PostingSpec) (*EntryView, error) {
	entryID, occAt, err := s.store.GetEntryRef(ctx, refType, refID)
	if err != nil {
		return nil, toAppErr(err)
	}
	existing, err := s.store.GetEntryWithPostings(ctx, entryID, occAt)
	if err != nil {
		return nil, toAppErr(err)
	}
	// Compare against requested payload resolved through account ids.
	requested := make([]domain.Posting, 0, len(specs))
	for _, spec := range specs {
		acct, err := s.store.GetAccountByRef(ctx, spec.Ref)
		if err != nil {
			return nil, toAppErr(err)
		}
		requested = append(requested, domain.Posting{AccountID: acct.ID, Amount: spec.Amount, Currency: spec.Currency})
	}
	if !samePayload(existing, requested) {
		return nil, toAppErr(domain.ErrDuplicateReference)
	}
	return existing, nil
}

func (s *Service) GetTransactionByReference(ctx context.Context, refType, refID string) (*EntryView, error) {
	entryID, occAt, err := s.store.GetEntryRef(ctx, refType, refID)
	if err != nil {
		return nil, toAppErr(err)
	}
	view, err := s.store.GetEntryWithPostings(ctx, entryID, occAt)
	if err != nil {
		return nil, toAppErr(err)
	}
	return view, nil
}

func (s *Service) GetBalances(ctx context.Context, refs []AccountRef) ([]BalanceRow, error) {
	if len(refs) == 0 {
		return nil, apperr.New(apperr.CodeInvalidArgument, "accounts required")
	}
	ids := make([]string, 0, len(refs))
	for _, ref := range refs {
		acct, err := s.store.GetAccountByRef(ctx, ref)
		if err != nil {
			return nil, toAppErr(err)
		}
		ids = append(ids, acct.ID)
	}
	rows, err := s.store.GetBalances(ctx, ids)
	if err != nil {
		return nil, toAppErr(err)
	}
	return rows, nil
}

// ListPostings requires a time range (partition pruning — ADR-0017).
func (s *Service) ListPostings(ctx context.Context, ref AccountRef, from, to time.Time, pageSize int32, cursor string) ([]PostingView, string, error) {
	if from.IsZero() || to.IsZero() || !from.Before(to) {
		return nil, "", apperr.New(apperr.CodeInvalidArgument, "a valid [from, to) time range is required")
	}
	if pageSize <= 0 {
		pageSize = defaultPageSize
	}
	if pageSize > maxPageSize {
		pageSize = maxPageSize
	}
	acct, err := s.store.GetAccountByRef(ctx, ref)
	if err != nil {
		return nil, "", toAppErr(err)
	}
	curTime, curID := to, uuidMax
	if cursor != "" {
		curTime, curID, err = decodeCursor(cursor)
		if err != nil {
			return nil, "", apperr.Wrap(apperr.CodeInvalidArgument, "malformed cursor", err)
		}
	}
	rows, err := s.store.ListPostings(ctx, acct.ID, from, to, pageSize+1, curTime, curID)
	if err != nil {
		return nil, "", toAppErr(err)
	}
	next := ""
	if len(rows) > int(pageSize) {
		rows = rows[:pageSize]
		last := rows[len(rows)-1]
		next = encodeCursor(last.OccurredAt, last.ID)
	}
	return rows, next, nil
}

func samePayload(existing *EntryView, requested []domain.Posting) bool {
	if len(existing.Postings) != len(requested) {
		return false
	}
	type leg struct {
		account  string
		amount   int64
		currency string
	}
	count := map[leg]int{}
	for _, p := range existing.Postings {
		count[leg{p.AccountID, p.Amount, p.Currency}]++
	}
	for _, p := range requested {
		count[leg{p.AccountID, p.Amount, p.Currency}]--
	}
	for _, n := range count {
		if n != 0 {
			return false
		}
	}
	return true
}

func addInt64(a, b int64) (int64, bool) {
	sum := a + b
	if (b > 0 && sum < a) || (b < 0 && sum > a) {
		return 0, false
	}
	return sum, true
}

func encodeCursor(t time.Time, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(t.UnixMicro(), 10) + "|" + id))
}

func decodeCursor(cur string) (time.Time, string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cur)
	if err != nil {
		return time.Time{}, "", err
	}
	parts := strings.SplitN(string(raw), "|", 2)
	if len(parts) != 2 {
		return time.Time{}, "", fmt.Errorf("bad cursor")
	}
	micros, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return time.Time{}, "", err
	}
	return time.UnixMicro(micros).UTC(), parts[1], nil
}

func entryToProto(v *EntryView) *ledgerv1.JournalEntry {
	out := &ledgerv1.JournalEntry{
		Id:            v.ID,
		ReferenceType: v.ReferenceType,
		ReferenceId:   v.ReferenceID,
		OccurredAt:    timestamppb.New(v.OccurredAt),
	}
	for _, p := range v.Postings {
		out.Postings = append(out.Postings, &ledgerv1.Posting{
			Id:                p.ID,
			EntryId:           p.EntryID,
			AccountId:         p.AccountID,
			ExternalAccountId: p.ExternalAccountID,
			Amount:            &commonv1.Money{MinorUnits: p.Amount, Currency: p.Currency},
			OccurredAt:        timestamppb.New(p.OccurredAt),
		})
	}
	return out
}
