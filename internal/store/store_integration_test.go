package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"theregister/internal/migrate"
	"theregister/internal/orders"
)

func openTestDatabase(t *testing.T) *sql.DB {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}

	admin, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() { _ = admin.Close() })

	schemaBytes := make([]byte, 8)
	if _, err := rand.Read(schemaBytes); err != nil {
		t.Fatalf("generate test schema name: %v", err)
	}
	schema := "test_" + hex.EncodeToString(schemaBytes)
	if _, err := admin.Exec(`CREATE SCHEMA "` + schema + `"`); err != nil {
		t.Fatalf("create test schema: %v", err)
	}

	config, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		t.Fatalf("parse test database URL: %v", err)
	}
	config.RuntimeParams["search_path"] = schema
	db := stdlib.OpenDB(*config)
	t.Cleanup(func() {
		_ = db.Close()
		if _, err := admin.Exec(`DROP SCHEMA "` + schema + `" CASCADE`); err != nil {
			t.Errorf("drop test schema: %v", err)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := migrate.Up(ctx, db); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	if err := migrate.Up(ctx, db); err != nil {
		t.Fatalf("reapply migrations: %v", err)
	}
	return db
}

func TestCreateOrderIsIdempotentAndRejectsChangedContent(t *testing.T) {
	db := openTestDatabase(t)
	store := New(db, []byte(strings.Repeat("h", 32)))
	ctx := context.Background()
	request, hash := decodeTestOrder(t, `{
		"order_ref":"order-idempotent",
		"occurred_at":"2026-11-21T12:00:00Z",
		"buyer":{"external_id":"buyer-a","type":"PERSON"},
		"seller":{"external_id":"seller-a","type":"ORGANIZATION"},
		"products":[]
	}`)

	first, err := store.CreateOrder(ctx, "partner-test", request, hash, "correlation-1")
	if err != nil {
		t.Fatalf("create order: %v", err)
	}
	second, err := store.CreateOrder(ctx, "partner-test", request, hash, "correlation-2")
	if err != nil {
		t.Fatalf("retry order: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("retry response differs:\nfirst: %#v\nsecond: %#v", first, second)
	}

	changed, changedHash := decodeTestOrder(t, `{
		"order_ref":"order-idempotent",
		"occurred_at":"2026-11-21T12:00:00Z",
		"buyer":{"external_id":"buyer-b","type":"PERSON"},
		"seller":{"external_id":"seller-a","type":"ORGANIZATION"},
		"products":[]
	}`)
	if _, err := store.CreateOrder(ctx, "partner-test", changed, changedHash, "correlation-3"); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("changed order error = %v, want ErrIdempotencyConflict", err)
	}
	changedType, changedTypeHash := decodeTestOrder(t, `{
		"order_ref":"order-idempotent",
		"occurred_at":"2026-11-21T12:00:00Z",
		"buyer":{"external_id":"buyer-a","type":"ORGANIZATION"},
		"seller":{"external_id":"seller-a","type":"ORGANIZATION"},
		"products":[]
	}`)
	if _, err := store.CreateOrder(ctx, "partner-test", changedType, changedTypeHash, "correlation-4"); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("changed party type error = %v, want ErrIdempotencyConflict", err)
	}

	var eventCount, auditCount, outboxCount int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM purchase_events WHERE order_ref = 'order-idempotent'`).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM audit_events WHERE entity_id = $1`, first.EventID).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM outbox_events WHERE aggregate_id = $1`, first.EventID).Scan(&outboxCount); err != nil {
		t.Fatal(err)
	}
	if eventCount != 1 || auditCount != 1 || outboxCount != 1 {
		t.Fatalf("duplicate side effects: events=%d audit=%d outbox=%d", eventCount, auditCount, outboxCount)
	}
}

func TestConcurrentRetriesCreateOneOrder(t *testing.T) {
	db := openTestDatabase(t)
	store := New(db, []byte(strings.Repeat("h", 32)))
	request, hash := decodeTestOrder(t, `{
		"order_ref":"order-concurrent",
		"occurred_at":"2026-11-21T12:00:00Z",
		"buyer":{"external_id":"buyer-a","type":"PERSON"},
		"seller":{"external_id":"seller-a","type":"ORGANIZATION"},
		"products":[]
	}`)

	const attempts = 12
	start := make(chan struct{})
	results := make([]orders.Response, attempts)
	errs := make([]error, attempts)
	var wait sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			results[index], errs[index] = store.CreateOrder(context.Background(), "partner-test", request, hash, "correlation")
		}(i)
	}
	close(start)
	wait.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("attempt %d: %v", i, err)
		}
		if i > 0 && !reflect.DeepEqual(results[0], results[i]) {
			t.Fatalf("attempt %d returned a different response", i)
		}
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM purchase_events WHERE order_ref = 'order-concurrent'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("purchase event count = %d, want 1", count)
	}
}

func TestAddProductsIsIdempotentAndRetainsProductIdentity(t *testing.T) {
	db := openTestDatabase(t)
	store := New(db, []byte(strings.Repeat("h", 32)))
	ctx := context.Background()
	order, orderHash := decodeTestOrder(t, `{
		"order_ref":"order-later-products",
		"occurred_at":"2026-11-21T12:00:00Z",
		"buyer":{"external_id":"buyer-a","type":"PERSON"},
		"seller":{"external_id":"seller-a","type":"ORGANIZATION"}
	}`)
	if _, err := store.CreateOrder(ctx, "partner-test", order, orderHash, "correlation-1"); err != nil {
		t.Fatalf("create empty order: %v", err)
	}

	products, productHash := decodeTestProducts(t, `{"event_ref":"addition-1","products":[{"identifiers":[{"scheme":"serial","value":"serial-42","qualifiers":{"gtin":"00012345678905"}}]}]}`)
	first, err := store.AddProducts(ctx, "partner-test", order.OrderRef, "addition-1", products, productHash, "correlation-2")
	if err != nil {
		t.Fatalf("add products: %v", err)
	}
	second, err := store.AddProducts(ctx, "partner-test", order.OrderRef, "addition-1", products, productHash, "correlation-3")
	if err != nil {
		t.Fatalf("retry product addition: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("product retry response differs:\nfirst: %#v\nsecond: %#v", first, second)
	}
	if len(first.Products) != 1 {
		t.Fatalf("product count = %d, want 1", len(first.Products))
	}

	var identifierCount, ownershipCount, additionCount int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM product_identifiers WHERE value = 'serial-42'`).Scan(&identifierCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM ownership_events WHERE product_instance_id = $1`, first.Products[0].ID).Scan(&ownershipCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM order_additions WHERE event_ref = 'addition-1'`).Scan(&additionCount); err != nil {
		t.Fatal(err)
	}
	if identifierCount != 1 || ownershipCount != 1 || additionCount != 1 {
		t.Fatalf("duplicate addition side effects: identifiers=%d ownership=%d additions=%d", identifierCount, ownershipCount, additionCount)
	}

	changedProducts, changedHash := decodeTestProducts(t, `{"event_ref":"addition-1","products":[{"identifiers":[{"scheme":"serial","value":"serial-43","qualifiers":{"gtin":"00012345678905"}}]}]}`)
	if _, err := store.AddProducts(ctx, "partner-test", order.OrderRef, "addition-1", changedProducts, changedHash, "correlation-4"); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("changed addition error = %v, want ErrIdempotencyConflict", err)
	}
}

func TestProductIdentitySurvivesOwnershipChange(t *testing.T) {
	db := openTestDatabase(t)
	store := New(db, []byte(strings.Repeat("h", 32)))
	ctx := context.Background()

	firstRequest, firstHash := decodeTestOrder(t, `{
		"order_ref":"owner-order-1",
		"occurred_at":"2026-11-21T12:00:00Z",
		"buyer":{"external_id":"buyer-a","type":"PERSON"},
		"seller":{"external_id":"seller-a","type":"ORGANIZATION"},
		"products":[{"identifiers":[{"scheme":"gs1","value":"00012345678905","qualifiers":{"serial":"serial-shared"}}]}]
	}`)
	first, err := store.CreateOrder(ctx, "partner-test", firstRequest, firstHash, "correlation-1")
	if err != nil {
		t.Fatalf("create first purchase: %v", err)
	}

	secondRequest, secondHash := decodeTestOrder(t, `{
		"order_ref":"owner-order-2",
		"occurred_at":"2026-11-21T13:00:00Z",
		"buyer":{"external_id":"buyer-b","type":"PERSON"},
		"seller":{"external_id":"seller-a","type":"ORGANIZATION"},
		"products":[{"identifiers":[{"scheme":"gs1","value":"00012345678905","qualifiers":{"serial":"serial-shared"}}]}]
	}`)
	second, err := store.CreateOrder(ctx, "partner-test", secondRequest, secondHash, "correlation-2")
	if err != nil {
		t.Fatalf("create second purchase: %v", err)
	}
	if first.Products[0].ID != second.Products[0].ID {
		t.Fatalf("product identity changed between purchases: %s != %s", first.Products[0].ID, second.Products[0].ID)
	}

	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM ownership_events WHERE product_instance_id = $1`, first.Products[0].ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("ownership event count = %d, want 2", count)
	}
}

func decodeTestOrder(t *testing.T, body string) (orders.Request, [32]byte) {
	t.Helper()
	request, hash, err := orders.DecodeRequest([]byte(body))
	if err != nil {
		t.Fatalf("decode test order: %v", err)
	}
	return request, hash
}

func decodeTestProducts(t *testing.T, body string) ([]orders.Product, [32]byte) {
	t.Helper()
	_, products, hash, err := orders.DecodeProductAddition([]byte(body))
	if err != nil {
		t.Fatalf("decode test product addition: %v", err)
	}
	return products, hash
}
