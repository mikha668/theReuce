# Partner API guide

The partner API registers an order and optionally its physical products. Select a partner in the API console to use its key, or send `Authorization: Bearer <partner-api-key>`. Requests use JSON and responses include `X-Correlation-ID`.

The formal contract is [openapi.yaml](../openapi/openapi.yaml). The API console provides editable templates and field details.

## Money and VAT

- Every new order requires `currency`, `total_amount`, and `vat_amount`. Totals include VAT; `vat_amount` is the part of the total that is VAT, not a percentage.
- `currency` is a three-letter code such as `SEK`, shared by the order and its products.
- Amounts are non-negative JSON numbers (not strings), with at most 15 digits before and four digits after the decimal point. Exponent notation is not accepted. For example, use `125.50`, not `"125.50"` or `1.255e2`.
- VAT must not exceed the corresponding total. Each product on a new order must provide its own total and VAT amount.
- An order may have no products or only some products. Across all products attached to that order, the sum of totals, VAT, and amounts excluding VAT cannot exceed the corresponding order amounts. When all products have been registered, their totals and VAT should equal the order amounts. The API does not currently have a finalization step to enforce completion.
- Products are individual physical instances, not quantities. Price each instance separately. Shipping, discounts, and other amounts are not modeled separately; allocate them to the product totals if the products are to add up to the order total.
- Orders created before amount support retain their original data and can still be retried. They have no recorded total or currency, so amount limits cannot be checked for later additions to those legacy orders.

## Create an order

`POST /partner/v1/orders`

Required fields:

| Field | What to provide |
| --- | --- |
| `order_ref` | Non-empty reference unique to this partner, up to 200 characters. |
| `occurred_at` | Purchase time as an RFC 3339 date/time, for example `2026-11-21T14:00:00Z`. |
| `currency` | Three-letter currency code. |
| `total_amount` | Order total including VAT. |
| `vat_amount` | VAT included in the order total. Use `0` if there is no VAT. |
| `buyer`, `seller` | Objects with `external_id` and `type`; type is `PERSON`, `ORGANIZATION`, or `UNKNOWN`. |

`products` is **optional** and may be omitted or `[]`. This is a complete order without products:

```json
{
  "order_ref": "order-100",
  "occurred_at": "2026-11-21T14:00:00Z",
  "currency": "SEK",
  "total_amount": 125.00,
  "vat_amount": 25.00,
  "buyer": { "external_id": "buyer-42", "type": "PERSON" },
  "seller": { "external_id": "store-7", "type": "ORGANIZATION" },
  "products": []
}
```

To include products in the same order, replace `products` with a list such as:

```json
[
  {
    "manufacturer": "Example manufacturer",
    "model": "Example model",
    "total_amount": 125.00,
    "vat_amount": 25.00,
    "identifiers": [
      { "scheme": "serial", "value": "serial-123" }
    ]
  }
]
```

For each product:

| Field | Required | What to provide |
| --- | --- | --- |
| `total_amount` | Yes | Product total including VAT, in the order currency. |
| `vat_amount` | Yes | VAT included in the product total. |
| `identifiers` | Yes | At least one identifier object. |
| `identifiers[].scheme` | Yes | Starts with an ASCII letter or digit; then letters, digits, `.`, `_`, or `-`, maximum 64 characters. Example: `serial`. |
| `identifiers[].value` | Yes | Non-empty identifier value, maximum 512 characters. |
| `identifiers[].qualifiers` | No | Object of string key/value pairs. |
| `manufacturer`, `model` | No | Product description strings. |
| `resources` | No | Array of external resources; omit it or use `[]` if not needed. |

An optional resource looks like:

```json
{
  "type": "MANUAL",
  "uri": "https://example.com/manual",
  "access_level": "OWNER",
  "resolver_type": "DIRECT_HTTPS",
  "media_type": "application/pdf",
  "language": "en"
}
```

For each resource, `type`, `uri`, `access_level`, and `resolver_type` are required. The URI must be HTTPS without user information. See the [OpenAPI schema](../openapi/openapi.yaml) for allowed values.

## Add products later

`POST /partner/v1/orders/{order_ref}/products`

Replace `{order_ref}` with the URL-encoded reference of an order belonging to the authenticated partner. Send a new partner-unique `event_ref` and at least one product:

```json
{
  "event_ref": "order-100-products-1",
  "products": [
    {
      "total_amount": 125.00,
      "vat_amount": 25.00,
      "identifiers": [{ "scheme": "serial", "value": "serial-123" }]
    }
  ]
}
```

If adding products in several requests, each addition may be partial, but the accumulated amounts must stay within the order amounts. A product cannot be added twice to the same order using different events.

## Responses and retries

- `202 Accepted`: the event was recorded. The response includes `event_id`, `status`, `order_ref`, `received_at`, and product IDs.
- `400 Bad Request`: invalid or missing values, invalid identifiers or URLs, or product amounts exceeding the order amounts. The response's `detail` explains the error.
- `401 Unauthorized`: the partner key is missing or invalid.
- `404 Not Found`: the order does not exist for the authenticated partner.
- `409 Conflict`: an order or event reference was reused with different content, a product is already attached, or product identifiers conflict.
- `500` or `503`: the service or a dependency failed; use the correlation ID to find the request in logs.

Retry an unchanged order with the same `order_ref`, or an unchanged addition with the same `event_ref`; the original response is returned. Changing the content under the same reference returns `409`.
