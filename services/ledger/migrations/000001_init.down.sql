DROP TABLE IF EXISTS outbox;
DROP TABLE IF EXISTS holds;
DROP TABLE IF EXISTS postings;
DROP TABLE IF EXISTS journal_entry_refs;
DROP TABLE IF EXISTS journal_entries;
DROP TABLE IF EXISTS account_balances;
DROP TABLE IF EXISTS ledger_accounts;
DROP FUNCTION IF EXISTS check_entry_zero_sum();
