// Package app orchestrates the transfer saga (ADR-0010, ADR-0012): create
// with business-level idempotency, drive hold → post → complete with
// compensation, recover ambiguous outcomes.
package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"time"

	commonv1 "github.com/aidostt/bank-core/gen/go/bank/common/v1"
	eventsv1 "github.com/aidostt/bank-core/gen/go/bank/events/v1"
	transferv1 "github.com/aidostt/bank-core/gen/go/bank/transfer/v1"
	"github.com/aidostt/bank-core/pkg/apperr"
	"github.com/aidostt/bank-core/pkg/grpcx"
	"github.com/aidostt/bank-core/pkg/logging"
	"github.com/aidostt/bank-core/pkg/metrics"
	"github.com/aidostt/bank-core/pkg/money"
	"github.com/aidostt/bank-core/pkg/outbox"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/aidostt/bank-core/services/transfer/internal/domain"
)

const (
	TopicTransfersStatus = "transfers.status"
	ratePairUSDKZT       = "USDKZT"
	defaultTier          = "default"
	referenceType        = "transfer"
)

type Service struct {
	store   Store
	ledger  LedgerClient
	account AccountClient
	log     *slog.Logger
}

func NewService(store Store, ledger LedgerClient, account AccountClient, log *slog.Logger) *Service {
	return &Service{store: store, ledger: ledger, account: account, log: log}
}

type CreateCmd struct {
	CustomerID      string
	IdempotencyKey  string
	Type            domain.Type
	FromAccountID   string
	ToAccountID     string
	ToAccountNumber string
	Amount          int64
	Currency        string
}

func (c CreateCmd) hash() string {
	sum := sha256.Sum256(fmt.Appendf(nil, "%s|%s|%s|%s|%d|%s",
		c.Type, c.FromAccountID, c.ToAccountID, c.ToAccountNumber, c.Amount, c.Currency))
	return hex.EncodeToString(sum[:])
}

// CreateTransfer persists the transfer + idempotency key atomically, then
// drives the saga synchronously (happy path completes in-request; an
// ambiguous downstream timeout returns the current non-terminal state and
// the recovery worker finishes the job — transfer doc).
func (s *Service) CreateTransfer(ctx context.Context, cmd CreateCmd) (*Transfer, error) {
	if cmd.CustomerID == "" {
		return nil, apperr.New(apperr.CodeUnauthenticated, "no caller identity")
	}
	if cmd.IdempotencyKey == "" {
		return nil, apperr.New(apperr.CodeIdempotencyKeyRequired, "Idempotency-Key is required")
	}
	if err := domain.ValidateSpec(cmd.Type, cmd.FromAccountID, cmd.ToAccountID, cmd.ToAccountNumber, cmd.Amount, cmd.Currency); err != nil {
		return nil, apperr.Wrap(apperr.CodeInvalidArgument, err.Error(), err)
	}

	id, err := uuid.NewV7()
	if err != nil {
		return nil, apperr.Wrap(apperr.CodeInternal, "uuid", err)
	}
	transferID := id.String()
	requestHash := cmd.hash()

	var existingID string
	err = s.store.WithinTx(ctx, func(ctx context.Context, tx StoreTx) error {
		inserted, err := tx.TryInsertIdempotencyKey(ctx, cmd.CustomerID, cmd.IdempotencyKey, transferID, requestHash)
		if err != nil {
			return err
		}
		if !inserted {
			prevID, prevHash, err := tx.GetIdempotencyKey(ctx, cmd.CustomerID, cmd.IdempotencyKey)
			if err != nil {
				return err
			}
			if prevHash != requestHash {
				return apperr.New(apperr.CodeIdempotencyConflict,
					"idempotency key was already used with a different request")
			}
			existingID = prevID
			return nil
		}
		t := &Transfer{
			ID: transferID, Type: cmd.Type, State: domain.StateCreated,
			CustomerID: cmd.CustomerID, FromAccountID: cmd.FromAccountID,
			ToAccountID: cmd.ToAccountID, ToAccountNumber: cmd.ToAccountNumber,
			Amount: cmd.Amount, Currency: cmd.Currency,
		}
		if err := tx.InsertTransfer(ctx, t); err != nil {
			return err
		}
		return tx.AppendEvent(ctx, transferID, domain.StateCreated, domain.StateCreated,
			map[string]any{"idempotency_key": cmd.IdempotencyKey})
	})
	if err != nil {
		return nil, err
	}
	if existingID != "" {
		// Repeat request: return the transfer in its current state; if it is
		// still in-flight, re-drive it (all downstream ops are idempotent).
		t, err := s.store.GetTransfer(ctx, existingID)
		if err != nil {
			return nil, err
		}
		if domain.IsTerminal(t.State) {
			return t, nil
		}
		return s.Drive(ctx, existingID)
	}
	return s.Drive(ctx, transferID)
}

// Drive advances the saga until it reaches a terminal state or an ambiguous
// outcome parks it for recovery. Safe to call concurrently: every state
// write is guarded by the expected previous state.
func (s *Service) Drive(ctx context.Context, transferID string) (*Transfer, error) {
	for {
		t, err := s.store.GetTransfer(ctx, transferID)
		if err != nil {
			return nil, err
		}
		switch t.State {
		case domain.StateCreated:
			if err := s.transition(ctx, t, domain.EvStart, nil, nil); err != nil {
				if errors.Is(err, ErrStateRaced) {
					continue
				}
				return nil, err
			}
		case domain.StateValidating:
			if parked, err := s.validate(ctx, t); err != nil {
				return nil, err
			} else if parked {
				return s.store.GetTransfer(ctx, transferID)
			}
		case domain.StateHeld:
			if parked, err := s.holdAndAdvance(ctx, t); err != nil {
				return nil, err
			} else if parked {
				return s.store.GetTransfer(ctx, transferID)
			}
		case domain.StatePosting:
			if parked, err := s.post(ctx, t); err != nil {
				return nil, err
			} else if parked {
				return s.store.GetTransfer(ctx, transferID)
			}
		case domain.StateReleasing:
			if parked, err := s.release(ctx, t); err != nil {
				return nil, err
			} else if parked {
				return s.store.GetTransfer(ctx, transferID)
			}
		case domain.StateCompleted, domain.StateFailed:
			return t, nil
		default:
			return nil, apperr.Newf(apperr.CodeInternal, "unknown state %s", t.State)
		}
	}
}

// validate resolves accounts, enforces ownership/status/limits and locks
// the FX rate. Returns parked=true when an upstream outage makes progress
// impossible right now (client retries via the same idempotency key).
func (s *Service) validate(ctx context.Context, t *Transfer) (bool, error) {
	fail := func(reason string) error {
		r := reason
		return s.transition(ctx, t, domain.EvValidationFailed, func(t *Transfer) { t.Reason = &r }, map[string]any{"reason": reason})
	}

	var from, to AccountInfo
	var err error
	switch t.Type {
	case domain.TypeTopup:
		to, err = s.account.GetAccount(ctx, t.ToAccountID)
	case domain.TypeInternal:
		from, err = s.account.GetAccount(ctx, t.FromAccountID)
		if err == nil {
			to, err = s.account.GetAccount(ctx, t.ToAccountID)
		}
	case domain.TypeP2P:
		from, err = s.account.GetAccount(ctx, t.FromAccountID)
		if err == nil {
			to, err = s.account.ResolveByNumber(ctx, t.ToAccountNumber)
		}
	}
	if err != nil {
		ae := apperr.FromGRPC(err)
		if ae.Code == apperr.CodeUpstreamUnavailable {
			return true, nil // parked in VALIDATING; retry re-drives
		}
		if ae.Code == apperr.CodeNotFound {
			return false, fail("ACCOUNT_NOT_FOUND")
		}
		if ae.Code == apperr.CodeForbidden {
			return false, fail("NOT_ACCOUNT_OWNER")
		}
		return false, err
	}

	// Ownership (architecture §6): the source account (and the TOPUP /
	// INTERNAL destination) must belong to the caller.
	if t.Type != domain.TypeTopup && from.UserID != t.CustomerID {
		return false, fail("NOT_ACCOUNT_OWNER")
	}
	if (t.Type == domain.TypeTopup || t.Type == domain.TypeInternal) && to.UserID != t.CustomerID {
		return false, fail("NOT_ACCOUNT_OWNER")
	}
	if t.Type != domain.TypeTopup && from.Status != "ACTIVE" {
		return false, fail("ACCOUNT_FROZEN")
	}
	if to.Status != "ACTIVE" {
		return false, fail("ACCOUNT_FROZEN")
	}
	if t.Type == domain.TypeP2P && from.ID == to.ID {
		return false, fail("SAME_ACCOUNT")
	}

	// Amount currency: source-account currency (destination for TOPUP).
	srcCurrency := from.Currency
	if t.Type == domain.TypeTopup {
		srcCurrency = to.Currency
	}
	if t.Currency != srcCurrency {
		return false, fail("CURRENCY_MISMATCH")
	}

	// FX lock (never for TOPUP — cash_in is per-currency).
	var counterAmount *int64
	var counterCurrency, ratePair *string
	var rateMicros *int64
	if t.Type != domain.TypeTopup && from.Currency != to.Currency {
		rate, err := s.store.GetLatestRate(ctx, ratePairUSDKZT, time.Now().UTC())
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				return false, fail("RATE_UNAVAILABLE")
			}
			return false, err
		}
		var counter int64
		var applied int64
		switch {
		case from.Currency == money.USD && to.Currency == money.KZT:
			applied = rate.BuyMicros
			counter, err = money.Convert(t.Amount, rate.BuyMicros)
		case from.Currency == money.KZT && to.Currency == money.USD:
			applied = rate.SellMicros
			counter, err = money.ConvertInverse(t.Amount, rate.SellMicros)
		default:
			return false, fail("UNSUPPORTED_CURRENCY_PAIR")
		}
		if err != nil {
			return false, fail("CONVERSION_FAILED")
		}
		if counter <= 0 {
			return false, fail("AMOUNT_TOO_SMALL")
		}
		counterAmount = &counter
		cc := to.Currency
		counterCurrency = &cc
		pair := rate.Pair
		ratePair = &pair
		rateMicros = &applied
	}

	// Limits (per-tx + daily) on outgoing transfers.
	if t.Type != domain.TypeTopup {
		perTx, daily, err := s.store.GetLimit(ctx, defaultTier, t.Currency)
		if err != nil {
			return false, err
		}
		since := time.Now().UTC().Truncate(24 * time.Hour)
		used, err := s.store.SumDailyOutgoing(ctx, t.CustomerID, t.Currency, since)
		if err != nil {
			return false, err
		}
		// The current transfer is already counted in SumDailyOutgoing
		// (it is persisted), so subtract it.
		if err := domain.CheckLimits(t.Amount, perTx, used-t.Amount, daily); err != nil {
			return false, fail("LIMIT_EXCEEDED")
		}
	}

	resolvedTo := to.ID
	err = s.transition(ctx, t, domain.EvValidated, func(t *Transfer) {
		t.ToAccountID = resolvedTo
		t.CounterAmount = counterAmount
		t.CounterCurrency = counterCurrency
		t.RateMicros = rateMicros
		t.RatePair = ratePair
	}, map[string]any{"to_account_id": resolvedTo})
	if errors.Is(err, ErrStateRaced) {
		return false, nil
	}
	return false, err
}

// holdAndAdvance: HELD was persisted before the ledger call — PlaceHold is
// idempotent by transfer id, so re-driving after a crash is safe.
func (s *Service) holdAndAdvance(ctx context.Context, t *Transfer) (bool, error) {
	if t.HoldID == nil {
		holdID, err := s.ledger.PlaceHold(ctx, s.sourceRef(t), t.Amount, t.Currency, t.ID)
		if err != nil {
			if apperr.Ambiguous(err) {
				return true, nil // parked in HELD; recovery re-drives
			}
			reason := string(apperr.FromGRPC(err).Code)
			terr := s.transition(ctx, t, domain.EvHoldFailed, func(t *Transfer) { t.Reason = &reason },
				map[string]any{"reason": reason})
			if terr != nil && !errors.Is(terr, ErrStateRaced) {
				return false, terr
			}
			return false, nil
		}
		err = s.transition(ctx, t, domain.EvPostingStarted, func(t *Transfer) { t.HoldID = &holdID },
			map[string]any{"hold_id": holdID})
		if err != nil && !errors.Is(err, ErrStateRaced) {
			return false, err
		}
		return false, nil
	}
	err := s.transition(ctx, t, domain.EvPostingStarted, nil, nil)
	if err != nil && !errors.Is(err, ErrStateRaced) {
		return false, err
	}
	return false, nil
}

func (s *Service) post(ctx context.Context, t *Transfer) (bool, error) {
	holdID := ""
	if t.HoldID != nil {
		holdID = *t.HoldID
	}
	err := s.ledger.PostTransaction(ctx, t.ID, holdID, s.buildPostings(t))
	if err != nil {
		if apperr.Ambiguous(err) {
			return true, nil // parked in POSTING; recovery disambiguates
		}
		reason := string(apperr.FromGRPC(err).Code)
		terr := s.transition(ctx, t, domain.EvPostFailed, func(t *Transfer) { t.Reason = &reason },
			map[string]any{"reason": reason})
		if terr != nil && !errors.Is(terr, ErrStateRaced) {
			return false, terr
		}
		return false, nil
	}
	terr := s.transition(ctx, t, domain.EvPosted, nil, nil)
	if terr != nil && !errors.Is(terr, ErrStateRaced) {
		return false, terr
	}
	return false, nil
}

func (s *Service) release(ctx context.Context, t *Transfer) (bool, error) {
	if t.HoldID != nil {
		if err := s.ledger.ReleaseHold(ctx, *t.HoldID); err != nil {
			ae := apperr.FromGRPC(err)
			if apperr.Ambiguous(err) {
				return true, nil // parked in RELEASING; recovery retries
			}
			// NotFound = hold never placed / already swept: fine.
			if ae.Code != apperr.CodeNotFound {
				return false, err
			}
		}
	}
	err := s.transition(ctx, t, domain.EvReleased, nil, nil)
	if err != nil && !errors.Is(err, ErrStateRaced) {
		return false, err
	}
	return false, nil
}

func (s *Service) sourceRef(t *Transfer) LedgerAccountRef {
	if t.Type == domain.TypeTopup {
		return LedgerAccountRef{InternalCode: cashInCode(t.Currency)}
	}
	return LedgerAccountRef{ExternalID: t.FromAccountID}
}

func cashInCode(currency string) string {
	if currency == money.USD {
		return "cash_in_usd"
	}
	return "cash_in_kzt"
}

func fxPositionCode(currency string) string {
	if currency == money.USD {
		return "fx_position_usd"
	}
	return "fx_position_kzt"
}

// buildPostings assembles the journal legs (architecture §4).
func (s *Service) buildPostings(t *Transfer) []LedgerPosting {
	src := s.sourceRef(t)
	dst := LedgerAccountRef{ExternalID: t.ToAccountID}
	if t.CounterAmount == nil {
		return []LedgerPosting{
			{Ref: src, Amount: -t.Amount, Currency: t.Currency},
			{Ref: dst, Amount: t.Amount, Currency: t.Currency},
		}
	}
	// Cross-currency: 4 postings, each currency sums to zero.
	return []LedgerPosting{
		{Ref: src, Amount: -t.Amount, Currency: t.Currency},
		{Ref: LedgerAccountRef{InternalCode: fxPositionCode(t.Currency)}, Amount: t.Amount, Currency: t.Currency},
		{Ref: LedgerAccountRef{InternalCode: fxPositionCode(*t.CounterCurrency)}, Amount: -*t.CounterAmount, Currency: *t.CounterCurrency},
		{Ref: dst, Amount: *t.CounterAmount, Currency: *t.CounterCurrency},
	}
}

// transition applies the pure state machine and persists atomically:
// guarded row update + transfer_events append + terminal outbox event in
// one DB transaction (transfer doc).
func (s *Service) transition(ctx context.Context, t *Transfer, event domain.Event, mutate func(*Transfer), detail map[string]any) error {
	next, err := domain.Next(t.State, event)
	if err != nil {
		return apperr.Wrap(apperr.CodeInternal,
			fmt.Sprintf("illegal transition %s + %s", t.State, event), err)
	}
	prev := t.State
	if mutate != nil {
		mutate(t)
	}
	t.State = next
	err = s.store.WithinTx(ctx, func(ctx context.Context, tx StoreTx) error {
		if err := tx.UpdateTransferGuarded(ctx, t, prev); err != nil {
			return err
		}
		if err := tx.AppendEvent(ctx, t.ID, prev, next, detail); err != nil {
			return err
		}
		if domain.IsTerminal(next) {
			return s.insertStatusEvent(ctx, tx, t)
		}
		return nil
	})
	if err != nil {
		t.State = prev // keep the in-memory copy honest for the caller loop
		return err
	}
	if domain.IsTerminal(next) {
		metrics.TransfersTotal.WithLabelValues(string(next)).Inc()
	}
	return nil
}

func (s *Service) insertStatusEvent(ctx context.Context, tx StoreTx, t *Transfer) error {
	view := ToProto(t)
	var msg outbox.Message
	var err error
	if t.State == domain.StateCompleted {
		msg, err = outbox.NewProtoMessage(ctx, TopicTransfersStatus, t.ID, logging.RequestID(ctx),
			&eventsv1.TransferCompleted{Transfer: view, AppliedRate: view.GetAppliedRate()})
	} else {
		reason := ""
		if t.Reason != nil {
			reason = *t.Reason
		}
		msg, err = outbox.NewProtoMessage(ctx, TopicTransfersStatus, t.ID, logging.RequestID(ctx),
			&eventsv1.TransferFailed{Transfer: view, Reason: reason})
	}
	if err != nil {
		return err
	}
	return tx.InsertOutbox(ctx, msg)
}

// GetTransfer is owner-scoped: customers see only their own transfers.
func (s *Service) GetTransfer(ctx context.Context, caller grpcx.Claims, id string) (*Transfer, error) {
	t, err := s.store.GetTransfer(ctx, id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, apperr.New(apperr.CodeNotFound, "transfer not found")
		}
		return nil, err
	}
	if !caller.IsStaff() && t.CustomerID != caller.CustomerID {
		return nil, apperr.New(apperr.CodeForbidden, "not your transfer")
	}
	return t, nil
}

func (s *Service) ListTransfers(ctx context.Context, caller grpcx.Claims, pageSize int32, cursor string) ([]*Transfer, string, error) {
	if caller.CustomerID == "" {
		return nil, "", apperr.New(apperr.CodeUnauthenticated, "no caller identity")
	}
	if pageSize <= 0 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}
	curTime := time.Now().UTC().Add(time.Hour)
	curID := "ffffffff-ffff-ffff-ffff-ffffffffffff"
	if cursor != "" {
		var err error
		curTime, curID, err = decodeCursor(cursor)
		if err != nil {
			return nil, "", apperr.Wrap(apperr.CodeInvalidArgument, "malformed cursor", err)
		}
	}
	rows, err := s.store.ListTransfersByCustomer(ctx, caller.CustomerID, pageSize+1, curTime, curID)
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(rows) > int(pageSize) {
		rows = rows[:pageSize]
		last := rows[len(rows)-1]
		next = encodeCursor(last.CreatedAt, last.ID)
	}
	return rows, next, nil
}

func (s *Service) GetRates(ctx context.Context) ([]Rate, error) {
	return s.store.ListLatestRates(ctx, time.Now().UTC())
}

// CleanupIdempotencyKeys deletes keys older than 24h (ADR-0012).
func (s *Service) CleanupIdempotencyKeys(ctx context.Context) {
	n, err := s.store.DeleteExpiredIdempotencyKeys(ctx, time.Now().UTC().Add(-24*time.Hour))
	if err != nil {
		if ctx.Err() == nil {
			s.log.Warn("idempotency cleanup failed", slog.String("error", err.Error()))
		}
		return
	}
	if n > 0 {
		s.log.Info("expired idempotency keys removed", slog.Int64("count", n))
	}
}

// ToProto converts to the transport view (also used for outbox payloads).
func ToProto(t *Transfer) *transferv1.TransferView {
	view := &transferv1.TransferView{
		Id:            t.ID,
		Type:          typeToProto(t.Type),
		State:         stateToProto(t.State),
		CustomerId:    t.CustomerID,
		FromAccountId: t.FromAccountID,
		ToAccountId:   t.ToAccountID,
		Amount:        &commonv1.Money{MinorUnits: t.Amount, Currency: t.Currency},
		CreatedAt:     timestamppb.New(t.CreatedAt),
		UpdatedAt:     timestamppb.New(t.UpdatedAt),
	}
	if t.CounterAmount != nil && t.CounterCurrency != nil {
		view.CounterAmount = &commonv1.Money{MinorUnits: *t.CounterAmount, Currency: *t.CounterCurrency}
	}
	if t.RateMicros != nil {
		view.AppliedRate = money.FormatRate(*t.RateMicros)
	}
	if t.Reason != nil {
		view.Reason = *t.Reason
	}
	return view
}

func typeToProto(t domain.Type) transferv1.TransferType {
	switch t {
	case domain.TypeTopup:
		return transferv1.TransferType_TRANSFER_TYPE_TOPUP
	case domain.TypeInternal:
		return transferv1.TransferType_TRANSFER_TYPE_INTERNAL
	case domain.TypeP2P:
		return transferv1.TransferType_TRANSFER_TYPE_P2P
	default:
		return transferv1.TransferType_TRANSFER_TYPE_UNSPECIFIED
	}
}

func stateToProto(s domain.State) transferv1.TransferState {
	switch s {
	case domain.StateCreated:
		return transferv1.TransferState_TRANSFER_STATE_CREATED
	case domain.StateValidating:
		return transferv1.TransferState_TRANSFER_STATE_VALIDATING
	case domain.StateHeld:
		return transferv1.TransferState_TRANSFER_STATE_HELD
	case domain.StatePosting:
		return transferv1.TransferState_TRANSFER_STATE_POSTING
	case domain.StateCompleted:
		return transferv1.TransferState_TRANSFER_STATE_COMPLETED
	case domain.StateReleasing:
		return transferv1.TransferState_TRANSFER_STATE_RELEASING
	case domain.StateFailed:
		return transferv1.TransferState_TRANSFER_STATE_FAILED
	default:
		return transferv1.TransferState_TRANSFER_STATE_UNSPECIFIED
	}
}
