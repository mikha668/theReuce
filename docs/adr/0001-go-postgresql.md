# ADR 0001: Go API and PostgreSQL

- Status: Accepted
- Date: 2026-11-21

## Context

The first release needs transactional order intake, stable product-instance identities, ownership history, append-only audit events, and a database-enforced idempotency boundary. The architecture proposal recommends Go and PostgreSQL and explicitly allows starting with SQL before adding distributed infrastructure.

## Decision

Use Go for the API and PostgreSQL as the system of record. Keep the initial application as a modular monolith with SQL migrations and database constraints. Do not introduce Kafka, Redis, or a second datastore in the first increment.

## Consequences

- Uniqueness and foreign-key constraints protect core identities and order references.
- A local PostgreSQL service is needed to run the application.
- PostgreSQL-specific migrations are not portable to SQLite.
- Add other infrastructure only when an observable requirement justifies it.
