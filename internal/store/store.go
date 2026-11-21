package store

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"theregister/internal/orders"
)

var (
	ErrIdempotencyConflict   = errors.New("idempotency reference already exists with different content")
	ErrIdentifierConflict    = errors.New("product identifier is already linked to a different product")
	ErrOrderNotFound         = errors.New("order not found")
	ErrInvalidAmounts        = errors.New("invalid amounts")
	ErrProductAlreadyOnOrder = errors.New("product is already attached to this order")
)

type Store struct {
	db               *sql.DB
	partyIdentityKey []byte
}

func New(db *sql.DB, partyIdentityKey []byte) *Store {
	return &Store{db: db, partyIdentityKey: partyIdentityKey}
}

func (s *Store) CreateOrder(ctx context.Context, partnerID string, request orders.Request, requestHash [32]byte, correlationID string) (orders.Response, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return orders.Response{}, fmt.Errorf("begin order transaction: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
		SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`,
		fmt.Sprintf("%d:%s:%s", len(partnerID), partnerID, request.OrderRef),
	); err != nil {
		return orders.Response{}, fmt.Errorf("lock order reference: %w", err)
	}
	var existingHash, responseJSON []byte
	err = tx.QueryRowContext(ctx, `
		SELECT payload_hash, response_body FROM purchase_events
		WHERE partner_id = $1 AND order_ref = $2`, partnerID, request.OrderRef,
	).Scan(&existingHash, &responseJSON)
	if err == nil {
		if !equalBytes(existingHash, requestHash[:]) {
			return orders.Response{}, ErrIdempotencyConflict
		}
		var response orders.Response
		if err := json.Unmarshal(responseJSON, &response); err != nil {
			return orders.Response{}, fmt.Errorf("decode saved order response: %w", err)
		}
		return response, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return orders.Response{}, fmt.Errorf("read existing purchase event: %w", err)
	}
	if err := orders.ValidateNewOrderAmounts(request); err != nil {
		return orders.Response{}, fmt.Errorf("%w: %v", ErrInvalidAmounts, err)
	}

	buyerID, err := s.upsertParty(ctx, tx, partnerID, request.Buyer)
	if err != nil {
		return orders.Response{}, err
	}
	sellerID, err := s.upsertParty(ctx, tx, partnerID, request.Seller)
	if err != nil {
		return orders.Response{}, err
	}

	var insertedID string
	err = tx.QueryRowContext(ctx, `
		INSERT INTO purchase_events
			(partner_id, order_ref, buyer_party_id, seller_party_id, occurred_at, payload_hash, correlation_id, currency, total_amount, vat_amount)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (partner_id, order_ref) DO NOTHING
		RETURNING id`,
		partnerID, request.OrderRef, buyerID, sellerID, request.OccurredAt, requestHash[:], correlationID,
		request.Currency, request.TotalAmount, request.VATAmount,
	).Scan(&insertedID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return orders.Response{}, fmt.Errorf("insert purchase event: %w", err)
	}
	if errors.Is(err, sql.ErrNoRows) {
		var existingHash, responseJSON []byte
		err = tx.QueryRowContext(ctx, `
			SELECT payload_hash, response_body
			FROM purchase_events
			WHERE partner_id = $1 AND order_ref = $2
			FOR UPDATE`, partnerID, request.OrderRef).Scan(&existingHash, &responseJSON)
		if err != nil {
			return orders.Response{}, fmt.Errorf("read existing purchase event: %w", err)
		}
		if !equalBytes(existingHash, requestHash[:]) {
			return orders.Response{}, ErrIdempotencyConflict
		}
		var response orders.Response
		if err := json.Unmarshal(responseJSON, &response); err != nil {
			return orders.Response{}, fmt.Errorf("decode saved order response: %w", err)
		}
		return response, nil
	}
	eventID := insertedID

	response := orders.Response{
		EventID:    eventID,
		Status:     "accepted",
		OrderRef:   request.OrderRef,
		ReceivedAt: time.Now().UTC(),
		Products:   make([]orders.ProductResult, 0, len(request.Products)),
	}
	for _, product := range request.Products {
		productID, err := s.addProduct(ctx, tx, partnerID, eventID, buyerID, product)
		if err != nil {
			return orders.Response{}, err
		}
		response.Products = append(response.Products, orders.ProductResult{ID: productID})
	}

	responseJSON, err = json.Marshal(response)
	if err != nil {
		return orders.Response{}, fmt.Errorf("encode order response: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE purchase_events SET response_body = $1::jsonb WHERE id = $2`,
		string(responseJSON), eventID,
	); err != nil {
		return orders.Response{}, fmt.Errorf("save order response: %w", err)
	}
	if err := appendAuditEvent(ctx, tx, partnerID, "purchase.registered", "purchase_event", eventID, correlationID, map[string]string{
		"order_ref": request.OrderRef,
	}); err != nil {
		return orders.Response{}, err
	}
	if err := enqueueOutboxEvent(ctx, tx, partnerID, "purchase.registered", eventID, correlationID, responseJSON); err != nil {
		return orders.Response{}, err
	}
	if err := tx.Commit(); err != nil {
		return orders.Response{}, fmt.Errorf("commit order transaction: %w", err)
	}
	return response, nil
}

func (s *Store) AddProducts(ctx context.Context, partnerID, orderRef, eventRef string, products []orders.Product, requestHash [32]byte, correlationID string) (orders.Response, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return orders.Response{}, fmt.Errorf("begin product addition transaction: %w", err)
	}
	defer tx.Rollback()

	var eventID, buyerID string
	var total, vat sql.NullString
	if err := tx.QueryRowContext(ctx, `
		SELECT id, buyer_party_id, total_amount::text, vat_amount::text FROM purchase_events
		WHERE partner_id = $1 AND order_ref = $2
		FOR UPDATE`, partnerID, orderRef).Scan(&eventID, &buyerID, &total, &vat); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return orders.Response{}, ErrOrderNotFound
		}
		return orders.Response{}, fmt.Errorf("read purchase event: %w", err)
	}

	var additionID string
	err = tx.QueryRowContext(ctx, `
		INSERT INTO order_additions
			(purchase_event_id, partner_id, event_ref, payload_hash, correlation_id)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (partner_id, event_ref) DO NOTHING
		RETURNING id`,
		eventID, partnerID, eventRef, requestHash[:], correlationID,
	).Scan(&additionID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return orders.Response{}, fmt.Errorf("insert product addition event: %w", err)
	}
	if errors.Is(err, sql.ErrNoRows) {
		var existingHash, responseJSON []byte
		var existingEventID string
		err = tx.QueryRowContext(ctx, `
			SELECT purchase_event_id, payload_hash, response_body FROM order_additions
			WHERE partner_id = $1 AND event_ref = $2 FOR UPDATE`,
			partnerID, eventRef,
		).Scan(&existingEventID, &existingHash, &responseJSON)
		if err != nil {
			return orders.Response{}, fmt.Errorf("read existing product addition: %w", err)
		}
		if existingEventID != eventID || !equalBytes(existingHash, requestHash[:]) {
			return orders.Response{}, ErrIdempotencyConflict
		}
		var response orders.Response
		if err := json.Unmarshal(responseJSON, &response); err != nil {
			return orders.Response{}, fmt.Errorf("decode saved product addition response: %w", err)
		}
		return response, nil
	}

	if total.Valid {
		var existingTotal, existingVAT string
		if err := tx.QueryRowContext(ctx, `
			SELECT COALESCE(SUM(total_amount), 0)::text, COALESCE(SUM(vat_amount), 0)::text
			FROM purchase_products WHERE purchase_event_id = $1`, eventID).Scan(&existingTotal, &existingVAT); err != nil {
			return orders.Response{}, fmt.Errorf("read purchase product amounts: %w", err)
		}
		orderTotal, orderVAT := orders.Amount(total.String), orders.Amount(vat.String)
		if err := orders.ValidateProductTotals(&orderTotal, &orderVAT, products, existingTotal, existingVAT); err != nil {
			return orders.Response{}, fmt.Errorf("%w: %v", ErrInvalidAmounts, err)
		}
	}

	response := orders.Response{
		EventID:    additionID,
		Status:     "accepted",
		OrderRef:   orderRef,
		ReceivedAt: time.Now().UTC(),
		Products:   make([]orders.ProductResult, 0, len(products)),
	}
	for _, product := range products {
		productID, err := s.addProduct(ctx, tx, partnerID, eventID, buyerID, product)
		if err != nil {
			return orders.Response{}, err
		}
		response.Products = append(response.Products, orders.ProductResult{ID: productID})
	}

	responseJSON, err := json.Marshal(response)
	if err != nil {
		return orders.Response{}, fmt.Errorf("encode product addition response: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE order_additions SET response_body = $1 WHERE id = $2`,
		string(responseJSON), additionID,
	); err != nil {
		return orders.Response{}, fmt.Errorf("save product addition response: %w", err)
	}
	if err := appendAuditEvent(ctx, tx, partnerID, "purchase.products_added", "purchase_event", eventID, correlationID, map[string]string{
		"event_ref": eventRef,
	}); err != nil {
		return orders.Response{}, err
	}
	if err := enqueueOutboxEvent(ctx, tx, partnerID, "purchase.products_added", additionID, correlationID, responseJSON); err != nil {
		return orders.Response{}, err
	}
	if err := tx.Commit(); err != nil {
		return orders.Response{}, fmt.Errorf("commit product addition: %w", err)
	}
	return response, nil
}

func (s *Store) addProduct(ctx context.Context, tx *sql.Tx, partnerID, purchaseEventID, buyerID string, product orders.Product) (string, error) {
	var productID string
	for _, identifier := range product.Identifiers {
		qualifiers, err := json.Marshal(identifier.Qualifiers)
		if err != nil {
			return "", fmt.Errorf("encode identifier qualifiers: %w", err)
		}
		var existingProductID string
		err = tx.QueryRowContext(ctx, `
			SELECT product_instance_id FROM product_identifiers
			WHERE scheme = $1 AND value = $2 AND qualifiers = $3::jsonb`,
			identifier.Scheme, identifier.Value, string(qualifiers),
		).Scan(&existingProductID)
		if err == nil {
			if productID != "" && productID != existingProductID {
				return "", ErrIdentifierConflict
			}
			productID = existingProductID
		} else if !errors.Is(err, sql.ErrNoRows) {
			return "", fmt.Errorf("find product identifier: %w", err)
		}
	}

	if productID == "" {
		if err := tx.QueryRowContext(ctx, `
			INSERT INTO product_instances (manufacturer, model)
			VALUES ($1, $2) RETURNING id`,
			product.Manufacturer, product.Model,
		).Scan(&productID); err != nil {
			return "", fmt.Errorf("create product instance: %w", err)
		}
	}

	for _, identifier := range product.Identifiers {
		qualifiers, err := json.Marshal(identifier.Qualifiers)
		if err != nil {
			return "", fmt.Errorf("encode identifier qualifiers: %w", err)
		}
		result, err := tx.ExecContext(ctx, `
			INSERT INTO product_identifiers (product_instance_id, scheme, value, qualifiers)
			VALUES ($1, $2, $3, $4::jsonb)
			ON CONFLICT (scheme, value, qualifiers) DO NOTHING`,
			productID, identifier.Scheme, identifier.Value, string(qualifiers),
		)
		if err != nil {
			return "", fmt.Errorf("save product identifier: %w", err)
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return "", fmt.Errorf("check product identifier: %w", err)
		}
		if rows == 0 {
			var ownerProductID string
			if err := tx.QueryRowContext(ctx, `
				SELECT product_instance_id FROM product_identifiers
				WHERE scheme = $1 AND value = $2 AND qualifiers = $3::jsonb`,
				identifier.Scheme, identifier.Value, string(qualifiers),
			).Scan(&ownerProductID); err != nil {
				return "", fmt.Errorf("verify product identifier: %w", err)
			}
			if ownerProductID != productID {
				return "", ErrIdentifierConflict
			}
		}
	}

	result, err := tx.ExecContext(ctx, `
		INSERT INTO purchase_products (purchase_event_id, product_instance_id, total_amount, vat_amount)
		VALUES ($1, $2, $3, $4) ON CONFLICT DO NOTHING`,
		purchaseEventID, productID, product.TotalAmount, product.VATAmount,
	)
	if err != nil {
		return "", fmt.Errorf("link product to purchase: %w", err)
	}
	linked, err := result.RowsAffected()
	if err != nil {
		return "", fmt.Errorf("check purchase product link: %w", err)
	}
	if linked == 0 && product.TotalAmount != nil {
		return "", ErrProductAlreadyOnOrder
	}
	var lockedProductID string
	if err := tx.QueryRowContext(ctx, `
		SELECT id FROM product_instances WHERE id = $1 FOR UPDATE`, productID,
	).Scan(&lockedProductID); err != nil {
		return "", fmt.Errorf("lock product instance: %w", err)
	}
	var nextOwnershipSequence int64
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(sequence), 0) + 1
		FROM ownership_events WHERE product_instance_id = $1`, productID,
	).Scan(&nextOwnershipSequence); err != nil {
		return "", fmt.Errorf("read product ownership sequence: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO ownership_events
			(product_instance_id, owner_party_id, source_purchase_event_id, event_type, sequence)
		VALUES ($1, $2, $3, 'PURCHASED', $4)
		ON CONFLICT (product_instance_id, source_purchase_event_id) DO NOTHING`,
		productID, buyerID, purchaseEventID, nextOwnershipSequence,
	); err != nil {
		return "", fmt.Errorf("record product ownership: %w", err)
	}
	for _, resource := range product.Resources {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO external_resources
				(product_instance_id, resource_type, uri, media_type, language, access_level, resolver_type, source_partner_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			productID, resource.Type, resource.URI, nullIfEmpty(resource.MediaType),
			nullIfEmpty(resource.Language), resource.AccessLevel, resource.Resolver, partnerID,
		); err != nil {
			return "", fmt.Errorf("save external resource: %w", err)
		}
	}
	return productID, nil
}

func (s *Store) upsertParty(ctx context.Context, tx *sql.Tx, partnerID string, party orders.Party) (string, error) {
	mac := hmac.New(sha256.New, s.partyIdentityKey)
	_, _ = mac.Write([]byte(partnerID))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(party.ExternalID))
	identityHash := mac.Sum(nil)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO parties (partner_id, external_id_hash, party_type)
		VALUES ($1, $2, $3)
		ON CONFLICT (partner_id, external_id_hash) DO NOTHING`,
		partnerID, identityHash, party.Type,
	); err != nil {
		return "", fmt.Errorf("register party: %w", err)
	}
	var storedID, storedType string
	if err := tx.QueryRowContext(ctx, `
		SELECT id, party_type FROM parties WHERE partner_id = $1 AND external_id_hash = $2`,
		partnerID, identityHash,
	).Scan(&storedID, &storedType); err != nil {
		return "", fmt.Errorf("read party: %w", err)
	}
	if storedType != party.Type {
		return "", errors.New("party was previously registered with a different type")
	}
	return storedID, nil
}

func appendAuditEvent(ctx context.Context, tx *sql.Tx, partnerID, eventType, entityType, entityID, correlationID string, details map[string]string) error {
	payload, err := json.Marshal(details)
	if err != nil {
		return fmt.Errorf("encode audit details: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO audit_events (partner_id, event_type, entity_type, entity_id, correlation_id, details)
		VALUES ($1, $2, $3, $4, $5, $6::jsonb)`,
		partnerID, eventType, entityType, entityID, correlationID, string(payload),
	); err != nil {
		return fmt.Errorf("append audit event: %w", err)
	}
	return nil
}

func enqueueOutboxEvent(ctx context.Context, tx *sql.Tx, partnerID, eventType, aggregateID, correlationID string, payload []byte) error {
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO outbox_events (partner_id, event_type, aggregate_id, correlation_id, payload)
		VALUES ($1, $2, $3, $4, $5::jsonb)`,
		partnerID, eventType, aggregateID, correlationID, string(payload),
	); err != nil {
		return fmt.Errorf("enqueue outbox event: %w", err)
	}
	return nil
}

func equalBytes(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func nullIfEmpty(value string) any {
	if value == "" {
		return nil
	}
	return value
}
