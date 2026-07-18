CREATE TABLE customers (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    uuid NOT NULL UNIQUE,
    tier       text NOT NULL DEFAULT 'default',
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE accounts (
    id          uuid PRIMARY KEY,
    customer_id uuid NOT NULL REFERENCES customers (id),
    number      text NOT NULL UNIQUE,
    currency    text NOT NULL CHECK (currency IN ('KZT', 'USD')),
    status      text NOT NULL DEFAULT 'ACTIVE' CHECK (status IN ('ACTIVE', 'FROZEN', 'CLOSED')),
    opened_at   timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_accounts_customer ON accounts (customer_id);

-- Eventually-consistent balance projection from ledger.transactions,
-- version-guarded (ADR-0006). The consumer ships in M2; the table is part of
-- the stable schema.
CREATE TABLE balances (
    account_id uuid PRIMARY KEY REFERENCES accounts (id),
    balance    bigint NOT NULL DEFAULT 0,
    version    bigint NOT NULL DEFAULT 0,
    as_of      timestamptz NOT NULL DEFAULT now()
);

-- Consumer dedup (ADR-0009); used by the M2 consumers.
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
