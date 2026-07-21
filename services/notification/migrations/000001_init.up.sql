-- Notification feed (docs/services/notification-service.md): doubles as the
-- user's notification history (read API is roadmap).
CREATE TABLE notifications (
    id         uuid PRIMARY KEY,
    user_id    uuid NOT NULL,
    channel    text NOT NULL DEFAULT 'log',
    template   text NOT NULL,
    payload    jsonb,
    body       text NOT NULL,
    status     text NOT NULL CHECK (status IN ('sent', 'failed')),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_notifications_user ON notifications (user_id, created_at DESC);

CREATE TABLE processed_messages (
    consumer_group text NOT NULL,
    event_id       uuid NOT NULL,
    processed_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (consumer_group, event_id)
);
