// Package app: the async scoring consumer (anti-fraud doc, ADR-0010 — no
// inline path). Consumes completed transfers, maintains per-customer stats
// in the consumer transaction and raises alerts through the outbox.
package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	eventsv1 "github.com/aidostt/bank-core/gen/go/bank/events/v1"
	transferv1 "github.com/aidostt/bank-core/gen/go/bank/transfer/v1"
	kafkart "github.com/aidostt/bank-core/pkg/kafka"
	"github.com/aidostt/bank-core/pkg/logging"
	"github.com/aidostt/bank-core/pkg/metrics"
	"github.com/aidostt/bank-core/pkg/outbox"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aidostt/bank-core/services/antifraud/internal/adapters/postgres/db"
	"github.com/aidostt/bank-core/services/antifraud/internal/domain"
)

const (
	Group               = "antifraud"
	TopicTransfersState = "transfers.status"
	TopicFraudAlerts    = "fraud.alerts"
)

type Scorer struct {
	log *slog.Logger
}

func NewScorer(log *slog.Logger) *Scorer { return &Scorer{log: log} }

// Handle runs inside the dedup transaction of the consumer runtime.
func (s *Scorer) Handle(ctx context.Context, tx pgx.Tx, msg kafkart.Message) error {
	payload := msg.Envelope.GetPayload()
	if !payload.MessageIs(&eventsv1.TransferCompleted{}) {
		return nil // failures and foreign event types are not scored
	}
	event := &eventsv1.TransferCompleted{}
	if err := payload.UnmarshalTo(event); err != nil {
		return fmt.Errorf("unmarshal TransferCompleted: %w", err)
	}
	view := event.GetTransfer()
	if view.GetType() == transferv1.TransferType_TRANSFER_TYPE_TOPUP {
		return nil // incoming mock funding is not outgoing spend
	}

	q := db.New(tx)
	rules, err := loadRules(ctx, q)
	if err != nil {
		return err
	}

	customerID := view.GetCustomerId()
	amount := view.GetAmount().GetMinorUnits()
	currency := view.GetAmount().GetCurrency()
	at := msg.OccurredAt
	if at.IsZero() {
		at = time.Now().UTC()
	}

	// Slide the windows (pure) over the stored stats.
	stats, err := q.GetStats(ctx, db.GetStatsParams{CustomerID: customerID, Currency: currency})
	winStart, winCount := time.Time{}, 0
	day, daySum := time.Time{}, int64(0)
	if err == nil {
		winStart, winCount = stats.Win5mStart, int(stats.Win5mCount)
		day, daySum = stats.Day, stats.DayOutSum
	} else if err != pgx.ErrNoRows {
		return err
	}
	winStart, winCount = domain.AdvanceWindow(winStart, winCount, at)
	day, daySum = domain.AdvanceDay(day, daySum, at, amount)

	newBeneficiary := false
	if view.GetToAccountId() != "" {
		n, err := q.TryInsertBeneficiary(ctx, db.TryInsertBeneficiaryParams{
			CustomerID: customerID, CounterpartyAccount: view.GetToAccountId(),
		})
		if err != nil {
			return err
		}
		newBeneficiary = n == 1
	}

	obs := domain.Observation{
		CustomerID:     customerID,
		TransferID:     view.GetId(),
		AccountID:      view.GetFromAccountId(),
		Amount:         amount,
		Currency:       currency,
		At:             at,
		Count5m:        winCount,
		DayOutSum:      daySum,
		NewBeneficiary: newBeneficiary,
	}
	for _, alert := range domain.Evaluate(rules, obs) {
		if err := s.raise(ctx, tx, q, obs, alert); err != nil {
			return err
		}
	}

	return q.UpsertStats(ctx, db.UpsertStatsParams{
		CustomerID: customerID, Currency: currency,
		Day: day, DayOutSum: daySum,
		Win5mStart: winStart, Win5mCount: int32(winCount), // #nosec G115 -- window counter, tiny
	})
}

func (s *Scorer) raise(ctx context.Context, tx pgx.Tx, q *db.Queries, obs domain.Observation, alert domain.Alert) error {
	id, err := uuid.NewV7()
	if err != nil {
		return err
	}
	details, err := json.Marshal(map[string]any{
		"detail": alert.Detail, "amount": obs.Amount, "currency": obs.Currency,
		"count_5m": obs.Count5m, "day_out_sum": obs.DayOutSum,
	})
	if err != nil {
		return err
	}
	if err := q.InsertAlert(ctx, db.InsertAlertParams{
		ID: id.String(), CustomerID: obs.CustomerID, TransferID: obs.TransferID,
		RuleID: alert.RuleID, Severity: alert.Severity, Details: details,
	}); err != nil {
		return err
	}
	msg, err := outbox.NewProtoMessage(ctx, TopicFraudAlerts, obs.CustomerID,
		logging.RequestID(ctx), &eventsv1.FraudAlertRaised{
			CustomerId: obs.CustomerID,
			TransferId: obs.TransferID,
			Rule:       alert.RuleID,
			Severity:   alert.Severity,
			Details:    string(details),
			AccountId:  obs.AccountID,
		})
	if err != nil {
		return err
	}
	if err := outbox.Insert(ctx, tx, msg); err != nil {
		return err
	}
	metrics.FraudAlertsTotal.WithLabelValues(alert.Severity).Inc()
	s.log.WarnContext(ctx, "fraud alert raised",
		slog.String("rule", alert.RuleID), slog.String("severity", alert.Severity),
		slog.String("customer.id", obs.CustomerID), slog.String("transfer.id", obs.TransferID))
	return nil
}

// loadRules parses the config-driven rules table into domain rules.
func loadRules(ctx context.Context, q *db.Queries) ([]domain.Rule, error) {
	rows, err := q.ListEnabledRules(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Rule, 0, len(rows))
	for _, r := range rows {
		rule := domain.Rule{ID: r.ID, Kind: r.Kind, Severity: r.Severity}
		var params struct {
			Thresholds map[string]int64 `json:"thresholds"`
			Limits     map[string]int64 `json:"limits"`
			MaxIn5m    int              `json:"max_in_5m"`
		}
		if err := json.Unmarshal(r.Params, &params); err != nil {
			return nil, fmt.Errorf("rule %s params: %w", r.ID, err)
		}
		rule.Thresholds = params.Thresholds
		if rule.Thresholds == nil {
			rule.Thresholds = params.Limits
		}
		rule.MaxIn5m = params.MaxIn5m
		out = append(out, rule)
	}
	return out, nil
}
