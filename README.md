# Product and Ownership Register

This repository starts with the partner-facing Go API and PostgreSQL schema. The first implemented flow records an order and its parties, product instances, identifiers, external resources, ownership events, audit events, and transactional outbox events.

An order may start with no products. Products can be attached later using a separate event reference, so retrying the original `order_ref` with changed content remains a `409 Conflict`.

## Requirements
 

- Go 1.23 or later
- PostgreSQL 13 or later
 (Work 4 myself!)
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

For frontend request logs, open the browser developer tools Console. API calls show the route, method, status, duration, and correlation ID; request bodies and keys are not logged. The API also writes structured request logs to its terminal, using route templates rather than identifying path values.

In the admin party form, enter comma-separated party aliases. Select a party in the list to edit its profile and aliases. Saving replaces the alias list; each alias must identify only one party.

The API console supplies an editable JSON template for each partner POST endpoint and lists required and optional fields. The order timestamp is prefilled with the current time; change it to the event's actual time. Products may be omitted or empty when creating an order, but adding products requires at least one product. Resources may be omitted or empty. See [the partner API guide](docs/partner-api.md) for complete examples and amount rules.

For an overview of the database tables and relationships, see the [database model](docs/database-model.md). For the screens, data flows, code structure and build instructions, see [How the application works](docs/application-guide.md).

## Partner API

Authenticate with `Authorization: Bearer <partner API key>`. Keys generated when creating a partner in the admin screen authenticate as that partner. The configured `PARTNER_API_KEY` and `PARTNER_ID` continue to work as a local fallback.

The admin API currently has no authentication and exposes partner keys. Run this prototype only in a trusted local environment; do not expose it to untrusted networks or use real personal data.

`POST /partner/v1/orders` creates an order. `products` may be omitted or empty:

```json
{
  "order_ref": "pos-847293",
  "occurred_at": "2026-11-21T14:00:00Z",
  "currency": "SEK",
  "total_amount": 125.00,
  "vat_amount": 25.00,
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

This is a demo API and browser UI, not a complete application, and is not ready for production or real personal data. It does not yet include authenticated admin or party access, ownership transfer, resolver execution, webhook delivery, OAuth/OIDC, or deployment TLS. See the architecture decisions under `docs/adr/`.
