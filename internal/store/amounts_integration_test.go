package store

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"theregister/internal/orders"
)

func TestOrderAmountLimitsAndPartialAdditions(t *testing.T) {
	db := openTestDatabase(t)
	s := New(db, []byte(strings.Repeat("h", 32)))
	ctx := context.Background()
	body := `{"order_ref":"amounts","occurred_at":"2026-01-01T00:00:00Z","currency":"SEK","total_amount":0.3,"vat_amount":0.06,"buyer":{"external_id":"b","type":"PERSON"},"seller":{"external_id":"s","type":"ORGANIZATION"},"products":[]}`
	order, hash, err := orders.DecodeRequest([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateOrder(ctx, "p", order, hash, "test"); err != nil {
		t.Fatal(err)
	}
	add := func(ref, serial, total, vat string) (orders.Response, error) {
		t.Helper()
		body := fmt.Sprintf(`{"event_ref":%q,"products":[{"total_amount":%s,"vat_amount":%s,"identifiers":[{"scheme":"serial","value":%q}]}]}`, ref, total, vat, serial)
		event, products, hash, err := orders.DecodeProductAddition([]byte(body))
		if err != nil {
			t.Fatal(err)
		}
		return s.AddProducts(ctx, "p", "amounts", event, products, hash, "test")
	}
	first, err := add("first", "one", "0.1", "0.02")
	if err != nil {
		t.Fatalf("partial addition: %v", err)
	}
	retry, err := add("first", "one", "0.10", "0.020")
	if err != nil || retry.EventID != first.EventID {
		t.Fatalf("equivalent retry: %v", err)
	}
	if _, err := add("too-large", "two", "0.21", "0.04"); !errors.Is(err, ErrInvalidAmounts) {
		t.Fatalf("excess total: %v", err)
	}
	if _, err := add("too-much-vat", "two", "0.2", "0.05"); !errors.Is(err, ErrInvalidAmounts) {
		t.Fatalf("excess VAT: %v", err)
	}
	if _, err := add("finish", "two", "0.2", "0.04"); err != nil {
		t.Fatalf("exact remaining amount: %v", err)
	}
	if _, err := add("duplicate-product", "one", "0", "0"); !errors.Is(err, ErrProductAlreadyOnOrder) {
		t.Fatalf("duplicate product: %v", err)
	}
	var total, vat string
	if err := db.QueryRow(`SELECT SUM(total_amount)::text, SUM(vat_amount)::text FROM purchase_products`).Scan(&total, &vat); err != nil || total != "0.3000" || vat != "0.0600" {
		t.Fatalf("persisted totals: %s %s, %v", total, vat, err)
	}
}

func TestConcurrentProductAmountsCannotExceedOrder(t *testing.T) {
	db := openTestDatabase(t)
	s := New(db, []byte(strings.Repeat("h", 32)))
	order, hash, err := orders.DecodeRequest([]byte(`{"order_ref":"concurrent-amounts","occurred_at":"2026-01-01T00:00:00Z","currency":"SEK","total_amount":100,"vat_amount":20,"buyer":{"external_id":"b","type":"PERSON"},"seller":{"external_id":"s","type":"ORGANIZATION"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateOrder(context.Background(), "p", order, hash, "test"); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	errs := make([]error, 2)
	var wait sync.WaitGroup
	for i := range errs {
		wait.Add(1)
		go func(i int) {
			defer wait.Done()
			body := fmt.Sprintf(`{"event_ref":"event-%d","products":[{"total_amount":60,"vat_amount":12,"identifiers":[{"scheme":"serial","value":"serial-%d"}]}]}`, i, i)
			event, products, hash, err := orders.DecodeProductAddition([]byte(body))
			if err != nil {
				errs[i] = err
				return
			}
			<-start
			_, errs[i] = s.AddProducts(context.Background(), "p", order.OrderRef, event, products, hash, "test")
		}(i)
	}
	close(start)
	wait.Wait()
	successes, rejected := 0, 0
	for _, err := range errs {
		if err == nil {
			successes++
		} else if errors.Is(err, ErrInvalidAmounts) {
			rejected++
		} else {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if successes != 1 || rejected != 1 {
		t.Fatalf("successes=%d, rejected=%d", successes, rejected)
	}
}

func TestLegacyOrderRetryAndNewOrderRequiresAmounts(t *testing.T) {
	db := openTestDatabase(t)
	s := New(db, []byte(strings.Repeat("h", 32)))
	body := `{"order_ref":"legacy","occurred_at":"2026-01-01T00:00:00Z","buyer":{"external_id":"b","type":"PERSON"},"seller":{"external_id":"s","type":"ORGANIZATION"}}`
	order, hash, err := orders.DecodeRequest([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	legacyJSON := `{"order_ref":"legacy","occurred_at":"2026-01-01T00:00:00Z","buyer":{"external_id":"b","type":"PERSON"},"seller":{"external_id":"s","type":"ORGANIZATION"},"products":[]}`
	if hash != sha256.Sum256([]byte(legacyJSON)) {
		t.Fatal("legacy idempotency hash changed")
	}
	if _, err := s.CreateOrder(context.Background(), "p", order, hash, "test"); !errors.Is(err, ErrInvalidAmounts) {
		t.Fatalf("new order without amounts: %v", err)
	}
	var buyer, seller string
	if err := db.QueryRow(`INSERT INTO parties (partner_id, external_id_hash, party_type) VALUES ('p', '\x01', 'PERSON') RETURNING id::text`).Scan(&buyer); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`INSERT INTO parties (partner_id, external_id_hash, party_type) VALUES ('p', '\x02', 'ORGANIZATION') RETURNING id::text`).Scan(&seller); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO purchase_events (partner_id, order_ref, buyer_party_id, seller_party_id, occurred_at, payload_hash, response_body, correlation_id)
		VALUES ('p', 'legacy', $1, $2, $3, $4, '{"event_id":"legacy","status":"accepted","order_ref":"legacy","received_at":"2026-01-01T00:00:00Z","products":[]}', 'test')`,
		buyer, seller, order.OccurredAt, hash[:]); err != nil {
		t.Fatal(err)
	}
	if result, err := s.CreateOrder(context.Background(), "p", order, hash, "test"); err != nil || result.EventID != "legacy" {
		t.Fatalf("legacy retry: %#v %v", result, err)
	}
}
