# ADR 0003: Single repository for the first application

- Status: Accepted
- Date: 2026-11-21

## Context

There is no application source yet. The initial deliverable is an API, with a browser interface expected later. Keeping the API contract, migrations, tests, and future UI together makes early changes easier to review.

## Decision

Keep the API, database migrations, OpenAPI contract, tests, and future frontend in one repository. Preserve clear module boundaries inside the repository.

## Consequences

- The API and future UI can share contract and development documentation.
- A frontend can be added without reorganizing the initial code.
- Split deployment or repositories only if team ownership or release cadence requires it.
