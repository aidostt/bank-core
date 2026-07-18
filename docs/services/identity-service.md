# identity-service

Users, credentials, tokens, sessions, roles. Related ADR: 0011.

## Responsibilities
- Register (email+password, argon2id hashing, basic profile: name, phone).
- Login → access JWT (RS256, 15m; claims sub/roles/sid) + refresh token (opaque
  256-bit, SHA-256 at rest, 30d).
- Refresh with rotation; reuse of a rotated token → revoke session family, audit log.
- Logout (revoke session), list/revoke own sessions.
- JWKS endpoint `/.well-known/jwks.json`; RSA keypair from mounted volume,
  generated on first boot; `kid` = key fingerprint; rotation runbook in this doc.
- Roles: `customer` by default; `support`/`admin` seeded via migration.
- On successful registration emits `customers.registered` (outbox) — consumed by
  account-service to prepare a customer record.

## gRPC API
`Register, Login, Refresh, Logout, GetMe, ListSessions, RevokeSession, Seed roles
(internal)`. Gateway is the only caller.

## Schema
`users(id, email uniq, password_hash, name, phone, created_at)` ·
`roles(user_id, role)` · `sessions(id, user_id, family_id, refresh_hash uniq,
rotated_from?, expires_at, revoked_at?, ip, user_agent, created_at)` · `outbox`.

## Testing / DoD
Unit: token claims, rotation logic, reuse-detection. Integration: full
register→login→refresh→reuse-attack scenario. DoD: reuse attack demonstrably kills
the family (e2e assertion); JWKS consumed by gateway in tests.
