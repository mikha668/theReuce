# Product and Ownership Register

This repository starts with the partner-facing Go API and PostgreSQL schema. The first implemented flow records an order and its parties, product instances, identifiers, external resources, ownership events, audit events, and transactional outbox events.

An order may start with no products. Products can be attached later using a separate event reference, so retrying the original `order_ref` with changed content remains a `409 Conflict`.

## Requirements

- Go 1.23 or later
- PostgreSQL 13 or later

## Configure and run

Set the connection string and independent, random credentials in PowerShell:

```powershell
$env:DATABASE_URL = "postgres://user:password@localhost:5432/product_register?sslmode=disable"
$env:PARTNER_ID = "local-partner"
$env:PARTNER_API_KEY = "<random partner token>"
$env:PARTY_ID_HMAC_KEY = "<at least 32 random bytes, encoded as text>"
```

Generate secret values with a trusted local secret generator; do not commit them. `PARTY_ID_HMAC_KEY` is used to store buyer and seller external identifiers as keyed hashes rather than plaintext.
Keep this key stable; rotating it requires a planned alias-hash migration or existing party matches will no longer resolve.

Apply the versioned SQL migrations and start the API:

```powershell
go mod download
go run ./cmd/migrate
go run ./cmd/api
```

The API listens on `:8080` by default; override it with `HTTP_ADDR`. Every response includes `X-Correlation-ID`. Supply a valid `X-Correlation-ID` request header to keep an existing trace identifier; otherwise one is generated.

## Partner API

Authenticate with `Authorization: Bearer <PARTNER_API_KEY>`.

`POST /partner/v1/orders` creates an order. `products` may be omitted or empty:

```json
{
  "order_ref": "pos-847293",
  "occurred_at": "2026-11-21T14:00:00Z",
  "buyer": { "external_id": "buyer-42", "type": "PERSON" },
  "seller": { "external_id": "store-7", "type": "ORGANIZATION" },
  "products": []
}
```

The same partner and `order_ref` with equivalent content returns the original accepted response. Reusing the reference with different content returns `409 Conflict`. The uniqueness check and order persistence run in one PostgreSQL transaction.

`POST /partner/v1/orders/{order_ref}/products` attaches products to an existing order. Its body must contain a new, partner-unique `event_ref` and at least one product. Retrying the same event with the same content returns its original response; changed content returns `409 Conflict`.

Product identifiers are scheme/value pairs with optional qualifiers. The current prototype does **not** yet enforce the `krav.txt` GTIN-plus-serial identity rule; do not rely on it for real inventory until identifier rules and cross-partner identity are implemented and tested. External resource URIs must use HTTPS and declare their resource type, resolver type, and access level. The API does not fetch external URIs.

`GET /health` checks process liveness. `GET /ready` checks PostgreSQL connectivity. Invalid requests use RFC 9457-style `application/problem+json` responses.

## Tests

```powershell
go test ./...
go vet ./...
```

Database-backed concurrency, migration, ownership-history, and idempotency tests run when `TEST_DATABASE_URL` points to a dedicated PostgreSQL test database. CI starts PostgreSQL and runs them. Local runs without that variable skip only the database integration tests.

## Current scope and next steps

This is an API foundation, not the complete application, and is not ready for production or real personal data. It does not yet include a browser UI, multi-partner credential management, owner/product read APIs, ownership transfer, resolver execution, webhook delivery, OAuth/OIDC, or deployment TLS. See the architecture decisions under `docs/adr/`.
