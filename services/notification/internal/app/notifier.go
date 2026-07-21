// Package app: the reference retry/DLQ consumer (notification doc) —
// renders templates for transfer and fraud events and "sends" them through
// the Sender port, persisting the feed row in the consumer transaction.
package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"text/template"

	eventsv1 "github.com/aidostt/bank-core/gen/go/bank/events/v1"
	kafkart "github.com/aidostt/bank-core/pkg/kafka"
	"github.com/aidostt/bank-core/pkg/metrics"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const (
	Group                = "notification"
	TopicTransfersStatus = "transfers.status"
	TopicFraudAlerts     = "fraud.alerts"
)

// Sender delivers a rendered notification. The log implementation prints a
// structured line; SMTP lives behind the same port (roadmap).
type Sender interface {
	Send(ctx context.Context, userID, body string) error
	Channel() string
}

// LogSender is the M2 mock delivery channel.
type LogSender struct{ Log *slog.Logger }

func (s LogSender) Channel() string { return "log" }

func (s LogSender) Send(ctx context.Context, userID, body string) error {
	s.Log.InfoContext(ctx, "notification sent",
		slog.String("user.id", userID), slog.String("body", body))
	return nil
}

// Templates per event type (EN default; RU/EN preference plumbing is
// roadmap — noted in the service doc).
var templates = template.Must(template.New("root").Parse(`
{{- define "transfer_completed" -}}
Your {{.Type}} transfer of {{.Amount}} is complete{{if .Counter}} — the beneficiary received {{.Counter}}{{end}}.
{{- end -}}
{{- define "transfer_failed" -}}
Your {{.Type}} transfer of {{.Amount}} failed: {{.Reason}}.
{{- end -}}
{{- define "fraud_alert" -}}
Suspicious activity detected on your account (rule {{.Rule}}, severity {{.Severity}}). {{if .High}}The affected account has been frozen — contact support.{{else}}No action is required; we are monitoring.{{end}}
{{- end -}}`))

// RenderData is the template input, exported for snapshot tests.
type RenderData struct {
	Type     string
	Amount   string
	Counter  string
	Reason   string
	Rule     string
	Severity string
	High     bool
}

func Render(name string, data RenderData) (string, error) {
	var buf bytes.Buffer
	if err := templates.ExecuteTemplate(&buf, name, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func formatMoney(units int64, currency string) string {
	sign := ""
	if units < 0 {
		sign, units = "-", -units
	}
	return fmt.Sprintf("%s%d.%02d %s", sign, units/100, units%100, currency)
}

type Notifier struct {
	sender Sender
	log    *slog.Logger
}

func NewNotifier(sender Sender, log *slog.Logger) *Notifier {
	return &Notifier{sender: sender, log: log}
}

// Handle runs inside the consumer's dedup transaction: the feed row commits
// atomically with the processed_messages marker — same event twice → one
// notification (notification doc DoD).
func (n *Notifier) Handle(ctx context.Context, tx pgx.Tx, msg kafkart.Message) error {
	payload := msg.Envelope.GetPayload()
	var (
		templateName string
		userID       string
		data         RenderData
	)
	switch {
	case payload.MessageIs(&eventsv1.TransferCompleted{}):
		event := &eventsv1.TransferCompleted{}
		if err := payload.UnmarshalTo(event); err != nil {
			return err
		}
		view := event.GetTransfer()
		userID = view.GetCustomerId()
		templateName = "transfer_completed"
		data = RenderData{
			Type:   view.GetType().String(),
			Amount: formatMoney(view.GetAmount().GetMinorUnits(), view.GetAmount().GetCurrency()),
		}
		if ca := view.GetCounterAmount(); ca.GetCurrency() != "" {
			data.Counter = formatMoney(ca.GetMinorUnits(), ca.GetCurrency())
		}
	case payload.MessageIs(&eventsv1.TransferFailed{}):
		event := &eventsv1.TransferFailed{}
		if err := payload.UnmarshalTo(event); err != nil {
			return err
		}
		view := event.GetTransfer()
		userID = view.GetCustomerId()
		templateName = "transfer_failed"
		data = RenderData{
			Type:   view.GetType().String(),
			Amount: formatMoney(view.GetAmount().GetMinorUnits(), view.GetAmount().GetCurrency()),
			Reason: event.GetReason(),
		}
	case payload.MessageIs(&eventsv1.FraudAlertRaised{}):
		event := &eventsv1.FraudAlertRaised{}
		if err := payload.UnmarshalTo(event); err != nil {
			return err
		}
		userID = event.GetCustomerId()
		templateName = "fraud_alert"
		data = RenderData{
			Rule: event.GetRule(), Severity: event.GetSeverity(),
			High: event.GetSeverity() == "HIGH",
		}
	default:
		return nil
	}
	if userID == "" {
		return nil
	}

	body, err := Render(templateName, data)
	if err != nil {
		return fmt.Errorf("render %s: %w", templateName, err)
	}
	status := "sent"
	if err := n.sender.Send(ctx, userID, body); err != nil {
		status = "failed"
	}

	id, err := uuid.NewV7()
	if err != nil {
		return err
	}
	rawPayload, err := json.Marshal(data)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO notifications (id, user_id, channel, template, payload, body, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		id.String(), userID, n.sender.Channel(), templateName, rawPayload, body, status); err != nil {
		return err
	}
	metrics.NotificationsTotal.WithLabelValues(status).Inc()
	return nil
}
