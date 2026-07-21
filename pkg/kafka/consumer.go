// Package kafka is the shared consumer runtime (ADR-0009, prompts/M2.md §1):
// a group consumer that hands every event to a handler inside a database
// transaction that also records the event id in processed_messages —
// duplicate deliveries become no-ops. Failures retry with exponential
// backoff (1→16s, 5 attempts) and then land in <group>.<topic>.dlq with
// error headers. One implementation, used by every consumer in bank-core.
package kafka

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	eventsv1 "github.com/aidostt/bank-core/gen/go/bank/events/v1"
	"github.com/aidostt/bank-core/pkg/logging"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/twmb/franz-go/pkg/kgo"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/protobuf/proto"
)

// Message is one decoded event delivered to a Handler.
type Message struct {
	EventID    string
	RequestID  string
	OccurredAt time.Time
	// Payload is the envelope's Any; handlers UnmarshalTo their event type.
	Envelope *eventsv1.EventEnvelope
	Topic    string
	Key      []byte
}

// Handler processes one event. It runs inside the same transaction that
// inserts the dedup row — every side effect through tx commits atomically
// with the dedup marker (ADR-0009). Returning an error triggers retry and,
// after the budget, DLQ.
type Handler func(ctx context.Context, tx pgx.Tx, msg Message) error

type Config struct {
	Brokers []string
	Group   string
	Topics  []string
	// MaxAttempts before DLQ; 0 = 5 (per architecture §5).
	MaxAttempts int
	// BackoffBase for attempt n: base×2^(n-1); 0 = 1s (1,2,4,8,16s).
	// Tests shrink this.
	BackoffBase time.Duration
}

type Consumer struct {
	client  *kgo.Client
	pool    *pgxpool.Pool
	cfg     Config
	handler Handler
	log     *slog.Logger
	lag     LagObserver
}

// LagObserver receives per-partition lag after each fetch; pkg/metrics
// provides the prometheus implementation. Nil-safe.
type LagObserver func(topic string, partition int32, lag int64)

func NewConsumer(cfg Config, pool *pgxpool.Pool, handler Handler, lag LagObserver, log *slog.Logger) (*Consumer, error) {
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 5
	}
	if cfg.BackoffBase <= 0 {
		cfg.BackoffBase = time.Second
	}
	client, err := kgo.NewClient(
		kgo.SeedBrokers(cfg.Brokers...),
		kgo.ConsumerGroup(cfg.Group),
		kgo.ConsumeTopics(cfg.Topics...),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
		kgo.AutoCommitMarks(),
		kgo.BlockRebalanceOnPoll(),
		kgo.AllowAutoTopicCreation(), // DLQ topics are created on first use
	)
	if err != nil {
		return nil, fmt.Errorf("kafka consumer client: %w", err)
	}
	return &Consumer{client: client, pool: pool, cfg: cfg, handler: handler, lag: lag, log: log}, nil
}

func (c *Consumer) Close() { c.client.Close() }

// Run blocks until ctx is done, processing records one at a time per
// partition batch (at-least-once; order preserved per partition).
func (c *Consumer) Run(ctx context.Context) {
	for {
		fetches := c.client.PollFetches(ctx)
		if ctx.Err() != nil {
			return
		}
		fetches.EachError(func(topic string, partition int32, err error) {
			if !errors.Is(err, context.Canceled) {
				c.log.Warn("kafka fetch error", slog.String("topic", topic),
					slog.Int("partition", int(partition)), slog.String("error", err.Error()))
			}
		})
		fetches.EachPartition(func(p kgo.FetchTopicPartition) {
			if c.lag != nil && len(p.Records) > 0 {
				last := p.Records[len(p.Records)-1]
				c.lag(p.Topic, p.Partition, p.HighWatermark-last.Offset-1)
			}
			for _, record := range p.Records {
				if done := c.processWithRetry(ctx, record); !done {
					// Shutdown mid-processing: leave the offset unmarked so
					// the record is redelivered after restart.
					return
				}
				c.client.MarkCommitRecords(record)
			}
		})
		c.client.AllowRebalance()
	}
}

// processWithRetry drives one record to success, dedup-skip or DLQ.
// Returns false only when shutdown interrupted processing — the caller must
// then NOT mark the offset, so the record is redelivered.
func (c *Consumer) processWithRetry(ctx context.Context, record *kgo.Record) bool {
	env := &eventsv1.EventEnvelope{}
	if err := proto.Unmarshal(record.Value, env); err != nil || env.GetEventId() == "" {
		// Undecodable payloads can never succeed — straight to DLQ.
		c.toDLQ(ctx, record, 0, time.Now().UTC(), fmt.Errorf("envelope decode: %w", err))
		return ctx.Err() == nil
	}
	msg := Message{
		EventID:    env.GetEventId(),
		RequestID:  env.GetRequestId(),
		OccurredAt: env.GetOccurredAt().AsTime(),
		Envelope:   env,
		Topic:      record.Topic,
		Key:        record.Key,
	}

	firstSeen := time.Now().UTC()
	var lastErr error
	for attempt := 1; attempt <= c.cfg.MaxAttempts; attempt++ {
		if attempt > 1 {
			backoff := c.cfg.BackoffBase << (attempt - 2) // 1,2,4,8,16 × base
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return false
			}
		}
		lastErr = c.processOnce(ctx, msg)
		if lastErr == nil {
			return true
		}
		if ctx.Err() != nil {
			return false
		}
		c.log.WarnContext(c.msgCtx(ctx, msg), "event handling failed",
			slog.String("topic", msg.Topic), slog.String("event.id", msg.EventID),
			slog.Int("attempt", attempt), slog.String("error", lastErr.Error()))
	}
	c.toDLQ(ctx, record, c.cfg.MaxAttempts, firstSeen, lastErr)
	return ctx.Err() == nil
}

// msgCtx restores correlation (request.id + trace context) from the envelope.
func (c *Consumer) msgCtx(ctx context.Context, msg Message) context.Context {
	ctx = logging.WithRequestID(ctx, msg.RequestID)
	if tc := msg.Envelope.GetTraceContext(); len(tc) > 0 {
		ctx = otel.GetTextMapPropagator().Extract(ctx, propagation.MapCarrier(tc))
	}
	return ctx
}

func (c *Consumer) processOnce(ctx context.Context, msg Message) error {
	ctx = c.msgCtx(ctx, msg)
	tracer := otel.Tracer("pkg/kafka")
	ctx, span := tracer.Start(ctx, msg.Topic+" process",
		trace.WithSpanKind(trace.SpanKindConsumer))
	defer span.End()

	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Dedup marker in the same transaction as the side effect (ADR-0009).
	tag, err := tx.Exec(ctx,
		`INSERT INTO processed_messages (consumer_group, event_id) VALUES ($1, $2)
		 ON CONFLICT (consumer_group, event_id) DO NOTHING`,
		c.cfg.Group, msg.EventID)
	if err != nil {
		return fmt.Errorf("dedup insert: %w", err)
	}
	if tag.RowsAffected() == 0 {
		c.log.DebugContext(ctx, "duplicate event skipped",
			slog.String("event.id", msg.EventID), slog.String("topic", msg.Topic))
		return tx.Commit(ctx)
	}
	if err := c.handler(ctx, tx, msg); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// toDLQ publishes the poisoned record to <group>.<topic>.dlq with error
// headers (architecture §5) and gives up on it.
func (c *Consumer) toDLQ(ctx context.Context, record *kgo.Record, attempts int, firstSeen time.Time, cause error) {
	dlqTopic := fmt.Sprintf("%s.%s.dlq", c.cfg.Group, record.Topic)
	errText := "unknown"
	if cause != nil {
		errText = cause.Error()
	}
	dlqRecord := &kgo.Record{
		Topic: dlqTopic,
		Key:   record.Key,
		Value: record.Value,
		Headers: []kgo.RecordHeader{
			{Key: "error", Value: []byte(errText)},
			{Key: "attempts", Value: []byte(fmt.Sprintf("%d", attempts))},
			{Key: "first_seen", Value: []byte(firstSeen.Format(time.RFC3339))},
			{Key: "source_topic", Value: []byte(record.Topic)},
		},
	}
	produceCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := c.client.ProduceSync(produceCtx, dlqRecord).FirstErr(); err != nil {
		// The record stays uncommitted-equivalent only if we crash here; we
		// log loudly — at-least-once means the DLQ produce is retried on the
		// next delivery of the same record after a restart.
		c.log.Error("DLQ produce failed — message may be lost from DLQ view",
			slog.String("topic", dlqTopic), slog.String("error", err.Error()))
		return
	}
	c.log.Warn("event moved to DLQ",
		slog.String("dlq", dlqTopic), slog.Int("attempts", attempts),
		slog.String("error", errText))
}
