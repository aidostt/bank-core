# Roadmap (deliberately out of scope — with intended designs)

Each item below was cut consciously (ADR-0001). One paragraph each so reviewers see
the thinking, not an accidental gap.

- **card-service** — virtual card issuing bound to accounts; PAN vaulting would be
  delegated to a tokenization provider; authorization flow reuses ledger holds.
- **statement-service** — monthly PDF statements built from `ListPostings` per
  detached partition; async job + object storage.
- **transaction-search-service** — CQRS read side: consume `ledger.transactions`
  into OpenSearch; the account-service projection already demonstrates the
  projection mechanic.
- **reporting-service** — regulator/BI exports from ledger partitions via read
  replica.
- **exchange-rate-service** — external rate feed (NBK) with caching and staleness
  policy; today rates are a seeded table in transfer (ADR-0001).
- **audit-service** — central append-only audit consumed from all topics; today
  audit lives in each service's append-only tables.
- **OAuth2/OIDC provider, MFA (TOTP), mTLS between services, Vault** — security
  hardening tier; JWKS/rotation design already anticipates it.
- **Debezium CDC outbox, Kafka proper cluster, per-service Postgres clusters, read
  replicas, account-hash sharding of ledger** — scaling tier; trigger points
  documented in ledger doc.
- **Terraform + managed k8s, Loki, OTel Collector, Pact contract tests, webhooks
  for partners, blue/green + canary via Argo Rollouts** — delivery tier.
