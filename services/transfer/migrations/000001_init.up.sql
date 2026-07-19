CREATE TABLE transfers (
    id                  uuid PRIMARY KEY,
    type                text NOT NULL CHECK (type IN ('TOPUP', 'INTERNAL', 'P2P')),
    state               text NOT NULL CHECK (state IN ('CREATED', 'VALIDATING', 'HELD', 'POSTING', 'COMPLETED', 'RELEASING', 'FAILED')),
    customer_id         uuid NOT NULL,
    from_account_id     uuid,
    to_account_id       uuid,
    to_account_number   text,
    amount              bigint NOT NULL CHECK (amount > 0),
    currency            text NOT NULL CHECK (currency IN ('KZT', 'USD')),
    counter_amount      bigint,
    counter_currency    text,
    applied_rate_micros bigint,
    rate_pair           text,
    reason              text,
    hold_id             uuid,
    recovery_attempts   int NOT NULL DEFAULT 0,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now()
);

-- Recovery scan (transfer doc): stuck non-terminal transfers by staleness.
CREATE INDEX idx_transfers_recovery ON transfers (state, updated_at);
CREATE INDEX idx_transfers_customer ON transfers (customer_id, created_at DESC);

-- Append-only audit of every state change (ADR-0010).
CREATE TABLE transfer_events (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    transfer_id uuid NOT NULL REFERENCES transfers (id),
    from_state  text NOT NULL,
    to_state    text NOT NULL,
    detail      jsonb,
    at          timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_transfer_events_transfer ON transfer_events (transfer_id, id);

-- Business-level idempotency (ADR-0012): written in the same tx that
-- creates the transfer; unique (customer_id, key). The FK is deferred so
-- the key can be claimed before the transfer row is inserted (fail-fast on
-- duplicates without orphan transfer rows).
CREATE TABLE idempotency_keys (
    customer_id  uuid NOT NULL,
    key          text NOT NULL,
    transfer_id  uuid NOT NULL REFERENCES transfers (id) DEFERRABLE INITIALLY DEFERRED,
    request_hash text NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (customer_id, key)
);

CREATE INDEX idx_idempotency_created ON idempotency_keys (created_at);

-- Seeded FX rates (ADR-0001): micro-scaled integers, never floats.
-- buy: bank buys USD (USD→KZT conversions), sell: bank sells USD (KZT→USD).
CREATE TABLE rates (
    pair        text NOT NULL,
    buy_micros  bigint NOT NULL CHECK (buy_micros > 0),
    sell_micros bigint NOT NULL CHECK (sell_micros > 0),
    valid_from  timestamptz NOT NULL,
    PRIMARY KEY (pair, valid_from)
);

INSERT INTO rates (pair, buy_micros, sell_micros, valid_from) VALUES
    ('USDKZT', 478250000, 482750000, '2026-01-01T00:00:00Z');

-- Per-tier limits, seeded for the default tier.
CREATE TABLE limits (
    tier     text NOT NULL,
    currency text NOT NULL,
    per_tx   bigint NOT NULL,
    daily    bigint NOT NULL,
    PRIMARY KEY (tier, currency)
);

INSERT INTO limits (tier, currency, per_tx, daily) VALUES
    ('default', 'KZT', 100000000, 500000000), -- 1M KZT per tx, 5M KZT daily
    ('default', 'USD',    200000,   1000000); -- $2k per tx, $10k daily

CREATE TABLE outbox (
    id         uuid PRIMARY KEY,
    topic      text NOT NULL,
    key        text NOT NULL,
    payload    bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    sent_at    timestamptz
);

CREATE INDEX idx_outbox_unsent ON outbox (created_at) WHERE sent_at IS NULL;
