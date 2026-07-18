# ADR-0001: Seven-service core scope

Status: accepted · Date: 2026-07-18

## Context
A full retail bank decomposes into 15+ services. A solo portfolio project must be
reviewable in ~10 minutes and finished, not sprawling. Depth on the money path
signals seniority better than breadth of scaffolds.

## Decision
Build 7 services: gateway, identity, account, ledger, transfer, antifraud,
notification. Everything else (cards, statements, reporting, search, exchange-rate,
audit, customer-profile) is documented in `docs/roadmap.md` with the intended design
in one paragraph each.

## Alternatives
15 thin services — rejected: signals cargo-cult microservices; unfinishable solo.
Monolith — rejected: the goal is to demonstrate distributed-systems competence.

## Consequences
Customer profile data is folded into identity (credentials) + account (ownership).
Payments and transfers are one service. FX rates are a seeded table inside transfer.
