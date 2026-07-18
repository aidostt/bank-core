// Package outbox implements the transactional outbox (ADR-0009): events are
// inserted in the same DB transaction as the business change; a relay
// goroutine publishes pending rows to Kafka and marks them sent.
package outbox

import (
	"context"
	"fmt"
	"time"

	eventsv1 "github.com/aidostt/bank-core/gen/go/bank/events/v1"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Message is one outbox row: an envelope-wrapped event bound for a topic.
type Message struct {
	ID      string
	Topic   string
	Key     string
	Payload []byte
}

// NewProtoMessage wraps payload in the standard envelope (event_id UUIDv7,
// occurred_at, request_id — architecture §5) and marshals it.
func NewProtoMessage(topic, key, requestID string, payload proto.Message) (Message, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return Message{}, fmt.Errorf("uuidv7: %w", err)
	}
	anyPayload, err := anypb.New(payload)
	if err != nil {
		return Message{}, fmt.Errorf("any: %w", err)
	}
	env := &eventsv1.EventEnvelope{
		EventId:    id.String(),
		OccurredAt: timestamppb.New(time.Now().UTC()),
		RequestId:  requestID,
		Payload:    anyPayload,
	}
	raw, err := proto.Marshal(env)
	if err != nil {
		return Message{}, fmt.Errorf("marshal envelope: %w", err)
	}
	return Message{ID: id.String(), Topic: topic, Key: key, Payload: raw}, nil
}

// Insert writes the message into the outbox table inside the caller's
// transaction. Direct produce from request handlers is forbidden
// (CLAUDE.md §5) — this is the only publish path.
func Insert(ctx context.Context, tx pgx.Tx, m Message) error {
	_, err := tx.Exec(ctx,
		`INSERT INTO outbox (id, topic, key, payload) VALUES ($1, $2, $3, $4)`,
		m.ID, m.Topic, m.Key, m.Payload)
	if err != nil {
		return fmt.Errorf("outbox insert: %w", err)
	}
	return nil
}
