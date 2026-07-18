CREATE TABLE users (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email         text NOT NULL UNIQUE,
    password_hash text NOT NULL,
    name          text NOT NULL,
    phone         text NOT NULL DEFAULT '',
    created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE roles (
    user_id uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    role    text NOT NULL CHECK (role IN ('customer', 'support', 'admin')),
    PRIMARY KEY (user_id, role)
);

-- Refresh sessions with rotation (ADR-0011). A session family groups all
-- rotations of one login; presenting an already-rotated token revokes the
-- whole family (theft detection).
CREATE TABLE sessions (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    family_id    uuid NOT NULL,
    refresh_hash bytea NOT NULL UNIQUE,
    rotated_from uuid,
    rotated_to   uuid,
    expires_at   timestamptz NOT NULL,
    revoked_at   timestamptz,
    ip           text NOT NULL DEFAULT '',
    user_agent   text NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_sessions_family ON sessions (family_id);
CREATE INDEX idx_sessions_user ON sessions (user_id);

-- Outbox table (ADR-0009). The customers.registered event + relay are wired
-- in M2 (see docs/services/identity-service.md, M1 note).
CREATE TABLE outbox (
    id         uuid PRIMARY KEY,
    topic      text NOT NULL,
    key        text NOT NULL,
    payload    bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    sent_at    timestamptz
);

CREATE INDEX idx_outbox_unsent ON outbox (created_at) WHERE sent_at IS NULL;

-- Seeded staff (ADR-0011): support/admin roles come from this migration, not
-- from the API. Password: "Adm1n-Demo-Pass" (argon2id, demo only).
INSERT INTO users (id, email, password_hash, name)
VALUES ('00000000-0000-7000-8000-000000000001',
        'admin@bank-core.local',
        '$argon2id$v=19$m=19456,t=2,p=1$YmFuay1jb3JlLWRlbW8tc2FsdA$2GrBhgFqycQKm5+mhYWbynBXquRC16eBEFGvULSZ/Xo',
        'Seeded Admin');

INSERT INTO roles (user_id, role) VALUES
    ('00000000-0000-7000-8000-000000000001', 'admin'),
    ('00000000-0000-7000-8000-000000000001', 'support'),
    ('00000000-0000-7000-8000-000000000001', 'customer');
