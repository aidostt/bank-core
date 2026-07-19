-- Ledger schema (ADR-0006, ADR-0007, ADR-0017).
-- journal_entries/postings are range-partitioned by month; monthly partitions
-- are created by a startup task, DEFAULT partitions are the safety net.

CREATE TABLE ledger_accounts (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    external_account_id uuid UNIQUE,
    internal_code       text UNIQUE,
    type                text NOT NULL CHECK (type IN ('customer', 'internal')),
    currency            text NOT NULL CHECK (currency IN ('KZT', 'USD')),
    status              text NOT NULL DEFAULT 'ACTIVE' CHECK (status IN ('ACTIVE', 'FROZEN', 'CLOSED')),
    created_at          timestamptz NOT NULL DEFAULT now(),
    CHECK ((type = 'customer' AND external_account_id IS NOT NULL)
        OR (type = 'internal' AND internal_code IS NOT NULL))
);

-- Materialized running balance, updated in the same transaction as postings
-- (ADR-0006). version increases by exactly 1 per balance change (invariant 5).
CREATE TABLE account_balances (
    account_id uuid PRIMARY KEY REFERENCES ledger_accounts (id),
    currency   text NOT NULL,
    balance    bigint NOT NULL DEFAULT 0,
    held       bigint NOT NULL DEFAULT 0 CHECK (held >= 0),
    version    bigint NOT NULL DEFAULT 0,
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE journal_entries (
    id             uuid NOT NULL,
    reference_type text NOT NULL,
    reference_id   text NOT NULL,
    occurred_at    timestamptz NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (id, occurred_at)
) PARTITION BY RANGE (occurred_at);

CREATE TABLE journal_entries_default PARTITION OF journal_entries DEFAULT;

-- Idempotency (invariant 6): one entry per (reference_type, reference_id).
-- A partitioned table cannot carry this unique index (it would need the
-- partition key), so the reference registry is a separate plain table
-- written in the same transaction.
CREATE TABLE journal_entry_refs (
    reference_type text NOT NULL,
    reference_id   text NOT NULL,
    entry_id       uuid NOT NULL,
    occurred_at    timestamptz NOT NULL,
    PRIMARY KEY (reference_type, reference_id)
);

CREATE TABLE postings (
    id          uuid NOT NULL,
    entry_id    uuid NOT NULL,
    account_id  uuid NOT NULL REFERENCES ledger_accounts (id),
    amount      bigint NOT NULL CHECK (amount <> 0),
    currency    text NOT NULL,
    occurred_at timestamptz NOT NULL,
    PRIMARY KEY (id, occurred_at),
    FOREIGN KEY (entry_id, occurred_at) REFERENCES journal_entries (id, occurred_at)
) PARTITION BY RANGE (occurred_at);

CREATE TABLE postings_default PARTITION OF postings DEFAULT;

CREATE INDEX idx_postings_account_time ON postings (account_id, occurred_at DESC);
CREATE INDEX idx_postings_entry ON postings (entry_id, occurred_at);

CREATE TABLE holds (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id     uuid NOT NULL REFERENCES ledger_accounts (id),
    amount         bigint NOT NULL CHECK (amount > 0),
    currency       text NOT NULL,
    reference_type text NOT NULL,
    reference_id   text NOT NULL,
    status         text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'captured', 'released')),
    expires_at     timestamptz NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    UNIQUE (reference_type, reference_id)
);

CREATE INDEX idx_holds_active_account ON holds (account_id) WHERE status = 'active';
CREATE INDEX idx_holds_active_expiry ON holds (expires_at) WHERE status = 'active';

CREATE TABLE outbox (
    id         uuid PRIMARY KEY,
    topic      text NOT NULL,
    key        text NOT NULL,
    payload    bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    sent_at    timestamptz
);

CREATE INDEX idx_outbox_unsent ON outbox (created_at) WHERE sent_at IS NULL;

-- DB-side belt-and-suspenders for invariant 1 (ADR-0006): at commit every
-- entry must sum to zero per currency. Constraint triggers are attached per
-- partition (plain tables); the startup partition task adds one to every new
-- monthly partition.
CREATE FUNCTION check_entry_zero_sum() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE bad int;
BEGIN
    SELECT count(*) INTO bad FROM (
        SELECT currency
        FROM postings
        WHERE entry_id = NEW.entry_id AND occurred_at = NEW.occurred_at
        GROUP BY currency
        HAVING sum(amount) <> 0
    ) s;
    IF bad > 0 THEN
        RAISE EXCEPTION 'journal entry % violates zero-sum per currency', NEW.entry_id;
    END IF;
    RETURN NULL;
END $$;

CREATE CONSTRAINT TRIGGER trg_zero_sum_default
    AFTER INSERT ON postings_default
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION check_entry_zero_sum();

-- Internal accounts (ADR-0006). cash_in is per-currency: every ledger
-- account has exactly one currency, so mock funding needs a KZT and a USD
-- source (documented in docs/services/ledger-service.md).
INSERT INTO ledger_accounts (id, internal_code, type, currency) VALUES
    ('00000000-0000-7000-8000-00000000ca01', 'cash_in_kzt',     'internal', 'KZT'),
    ('00000000-0000-7000-8000-00000000ca02', 'cash_in_usd',     'internal', 'USD'),
    ('00000000-0000-7000-8000-00000000f001', 'fx_position_kzt', 'internal', 'KZT'),
    ('00000000-0000-7000-8000-00000000f002', 'fx_position_usd', 'internal', 'USD');

INSERT INTO account_balances (account_id, currency)
SELECT id, currency FROM ledger_accounts WHERE type = 'internal';
