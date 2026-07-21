-- Rule-based async scoring (docs/services/anti-fraud-service.md).

-- Per-customer sliding stats, updated in the consumer transaction.
CREATE TABLE customer_stats (
    customer_id    uuid NOT NULL,
    currency       text NOT NULL,
    day            date NOT NULL,
    day_out_sum    bigint NOT NULL DEFAULT 0,
    win5m_start    timestamptz NOT NULL,
    win5m_count    int NOT NULL DEFAULT 0,
    updated_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (customer_id, currency)
);

CREATE TABLE alerts (
    id          uuid PRIMARY KEY,
    customer_id uuid NOT NULL,
    transfer_id uuid NOT NULL,
    rule_id     text NOT NULL,
    severity    text NOT NULL CHECK (severity IN ('MEDIUM', 'HIGH')),
    details     jsonb,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_alerts_customer ON alerts (customer_id, created_at DESC);

CREATE TABLE beneficiaries (
    customer_id          uuid NOT NULL,
    counterparty_account uuid NOT NULL,
    first_seen           timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (customer_id, counterparty_account)
);

-- Config-driven rules; params are jsonb so thresholds tune without deploys.
CREATE TABLE rules (
    id       text PRIMARY KEY,
    kind     text NOT NULL,
    params   jsonb NOT NULL,
    severity text NOT NULL CHECK (severity IN ('MEDIUM', 'HIGH')),
    enabled  boolean NOT NULL DEFAULT true
);

INSERT INTO rules (id, kind, params, severity) VALUES
    ('R1', 'amount_over',        '{"thresholds": {"KZT": 10000000, "USD": 100000}}', 'MEDIUM'),
    ('R2', 'velocity',           '{"max_in_5m": 10}',                                'HIGH'),
    ('R3', 'daily_out_sum',      '{"limits": {"KZT": 200000000, "USD": 500000}}',    'HIGH'),
    ('R4', 'new_beneficiary',    '{"thresholds": {"KZT": 5000000, "USD": 50000}}',   'MEDIUM');

CREATE TABLE processed_messages (
    consumer_group text NOT NULL,
    event_id       uuid NOT NULL,
    processed_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (consumer_group, event_id)
);

CREATE TABLE outbox (
    id         uuid PRIMARY KEY,
    topic      text NOT NULL,
    key        text NOT NULL,
    payload    bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    sent_at    timestamptz
);

CREATE INDEX idx_outbox_unsent ON outbox (created_at) WHERE sent_at IS NULL;
