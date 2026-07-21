// Package kafka wires account-service's three consumer concerns onto the
// shared runtime (prompts/M2.md §2-3): the version-guarded balance
// projection from ledger.transactions, the HIGH-severity freeze from
// fraud.alerts, and the customer bootstrap from customers.registered.
// One group ("account"), one handler routed by payload type.
package kafka

import (
	"context"
	"fmt"
	"log/slog"

	eventsv1 "github.com/aidostt/bank-core/gen/go/bank/events/v1"
	kafkart "github.com/aidostt/bank-core/pkg/kafka"
	"github.com/aidostt/bank-core/pkg/logging"
	"github.com/aidostt/bank-core/pkg/outbox"
	"github.com/jackc/pgx/v5"

	"github.com/aidostt/bank-core/services/account/internal/adapters/postgres/db"
	"github.com/aidostt/bank-core/services/account/internal/domain"
)

const (
	Group                    = "account"
	TopicLedgerTransactions  = "ledger.transactions"
	TopicFraudAlerts         = "fraud.alerts"
	TopicCustomersRegistered = "customers.registered"
	TopicAccountsEvents      = "accounts.events"
)

func Topics() []string {
	return []string{TopicLedgerTransactions, TopicFraudAlerts, TopicCustomersRegistered}
}

type Handlers struct {
	log *slog.Logger
}

func NewHandlers(log *slog.Logger) *Handlers { return &Handlers{log: log} }

// Handle dispatches by event payload type; runs inside the dedup tx.
func (h *Handlers) Handle(ctx context.Context, tx pgx.Tx, msg kafkart.Message) error {
	payload := msg.Envelope.GetPayload()
	switch {
	case payload.MessageIs(&eventsv1.TransactionPosted{}):
		event := &eventsv1.TransactionPosted{}
		if err := payload.UnmarshalTo(event); err != nil {
			return fmt.Errorf("unmarshal TransactionPosted: %w", err)
		}
		return h.applyProjection(ctx, tx, event)
	case payload.MessageIs(&eventsv1.FraudAlertRaised{}):
		event := &eventsv1.FraudAlertRaised{}
		if err := payload.UnmarshalTo(event); err != nil {
			return fmt.Errorf("unmarshal FraudAlertRaised: %w", err)
		}
		return h.applyFreeze(ctx, tx, msg, event)
	case payload.MessageIs(&eventsv1.CustomerRegistered{}):
		event := &eventsv1.CustomerRegistered{}
		if err := payload.UnmarshalTo(event); err != nil {
			return fmt.Errorf("unmarshal CustomerRegistered: %w", err)
		}
		return h.bootstrapCustomer(ctx, tx, event)
	default:
		// Not for us (e.g. an event type added later) — ack and move on.
		h.log.DebugContext(ctx, "ignoring unhandled event type",
			slog.String("type", payload.GetTypeUrl()), slog.String("topic", msg.Topic))
		return nil
	}
}

// applyProjection upserts the balance read model guarded by version —
// out-of-order deliveries converge (account doc; tested with v3 before v2).
func (h *Handlers) applyProjection(ctx context.Context, tx pgx.Tx, event *eventsv1.TransactionPosted) error {
	if event.GetExternalAccountId() == "" {
		return nil // internal ledger accounts are not customer-visible
	}
	q := db.New(tx)
	n, err := q.UpsertBalanceGuarded(ctx, db.UpsertBalanceGuardedParams{
		AccountID: event.GetExternalAccountId(),
		Balance:   event.GetBalanceAfter(),
		Version:   event.GetVersion(),
		AsOf:      event.GetEntry().GetOccurredAt().AsTime(),
	})
	if err != nil {
		return fmt.Errorf("projection upsert: %w", err)
	}
	if n == 0 {
		h.log.DebugContext(ctx, "stale projection event ignored",
			slog.String("account.id", event.GetExternalAccountId()),
			slog.Int64("version", event.GetVersion()))
	}
	return nil
}

// applyFreeze handles HIGH alerts: account → FROZEN + AccountFrozen event
// through the outbox in the same transaction (account doc).
func (h *Handlers) applyFreeze(ctx context.Context, tx pgx.Tx, msg kafkart.Message, event *eventsv1.FraudAlertRaised) error {
	if event.GetSeverity() != "HIGH" {
		return nil
	}
	if event.GetAccountId() == "" {
		h.log.WarnContext(ctx, "HIGH fraud alert without account id — nothing to freeze",
			slog.String("customer.id", event.GetCustomerId()))
		return nil
	}
	q := db.New(tx)
	current, err := q.GetAccountStatus(ctx, event.GetAccountId())
	if err != nil {
		if err == pgx.ErrNoRows {
			h.log.WarnContext(ctx, "fraud alert for unknown account",
				slog.String("account.id", event.GetAccountId()))
			return nil
		}
		return err
	}
	if current != domain.StatusActive {
		return nil // already frozen/closed — idempotent
	}
	if _, err := q.UpdateAccountStatus(ctx, db.UpdateAccountStatusParams{
		ID: event.GetAccountId(), Status: domain.StatusFrozen,
	}); err != nil {
		return fmt.Errorf("freeze: %w", err)
	}
	out, err := outbox.NewProtoMessage(ctx, TopicAccountsEvents, event.GetAccountId(),
		logging.RequestID(ctx), &eventsv1.AccountFrozen{
			AccountId: event.GetAccountId(),
			Reason:    fmt.Sprintf("fraud:%s", event.GetRule()),
		})
	if err != nil {
		return err
	}
	if err := outbox.Insert(ctx, tx, out); err != nil {
		return err
	}
	h.log.WarnContext(ctx, "account frozen by fraud alert",
		slog.String("account.id", event.GetAccountId()),
		slog.String("rule", event.GetRule()))
	return nil
}

// bootstrapCustomer prepares the customer row on registration (M2 replaces
// the M1 lazy-open path; the upsert keeps both idempotent).
func (h *Handlers) bootstrapCustomer(ctx context.Context, tx pgx.Tx, event *eventsv1.CustomerRegistered) error {
	if event.GetUserId() == "" {
		return nil
	}
	if _, err := db.New(tx).UpsertCustomer(ctx, event.GetUserId()); err != nil {
		return fmt.Errorf("customer bootstrap: %w", err)
	}
	return nil
}
