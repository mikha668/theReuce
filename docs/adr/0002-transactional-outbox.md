# ADR 0002: Transactional outbox

- Status: Accepted
- Date: 2026-11-21

## Context

Order acceptance must not be separated from recording events that will later drive webhooks or other asynchronous processing. Publishing directly to a broker before or after a database transaction can lose or duplicate notifications.

## Decision

Insert outbox records in the same PostgreSQL transaction as the purchase, ownership, and audit records. Keep the outbox as the durable publication boundary. A worker, retry schedule, signing, and dead-letter handling are a later increment.

## Consequences

- The database is the initial source of truth for pending events.
- A consumer must claim and mark outbox rows safely before delivery is enabled.
- Kafka is not required for the first API slice.
