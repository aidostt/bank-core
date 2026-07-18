-- One PostgreSQL container, one logical database + role per service (ADR-0004).
-- No cross-database access: each role owns exactly its own database.

CREATE USER identity_user WITH PASSWORD 'identity_pass';
CREATE DATABASE identity_db OWNER identity_user;

CREATE USER account_user WITH PASSWORD 'account_pass';
CREATE DATABASE account_db OWNER account_user;

CREATE USER ledger_user WITH PASSWORD 'ledger_pass';
CREATE DATABASE ledger_db OWNER ledger_user;

CREATE USER transfer_user WITH PASSWORD 'transfer_pass';
CREATE DATABASE transfer_db OWNER transfer_user;

CREATE USER antifraud_user WITH PASSWORD 'antifraud_pass';
CREATE DATABASE antifraud_db OWNER antifraud_user;

CREATE USER notification_user WITH PASSWORD 'notification_pass';
CREATE DATABASE notification_db OWNER notification_user;
