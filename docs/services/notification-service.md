# notification-service

Terminal consumer: turns events into (mock) user notifications. The reference
implementation of the retry/DLQ consumer pattern — kept deliberately simple.

## Responsibilities
- Consume `transfers.status` and `fraud.alerts`.
- Render templates (Go text/template, per event type, RU/EN by user preference
  default EN) and "send" via a `Sender` interface: `log` implementation prints a
  structured line; SMTP stub behind the same interface (roadmap).
- Persist `notifications(id, user_id, channel, template, payload jsonb, status
  sent|failed, created_at)` — doubles as the user's notification feed (roadmap API).
- Dedup, backoff retries, DLQ — via shared `pkg/kafka` consumer runtime.

## Testing / DoD
Template snapshot tests · dedup integration test (same event twice → one
notification) · e2e asserts the P2P completion notification row exists.
