package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"theregister/internal/store"
)

func TestPartnerDatabaseKeysAndOrderIsolation(t *testing.T) {
	db := openProfileTestDatabase(t)
	handler := NewServer(db, store.New(db, []byte(strings.Repeat("x", 32))), "configured-key", "configured-partner", nil)
	request := func(path, token, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response
	}
	createPartner := func(name string) string {
		t.Helper()
		response := request("/api/v1/partners", "", `{"name":"`+name+`","partner_id":"`+name+`"}`)
		if response.Code != http.StatusCreated {
			t.Fatalf("create partner: %d %s", response.Code, response.Body)
		}
		var result struct {
			APIKey string `json:"api_key"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || len(result.APIKey) < 40 {
			t.Fatalf("invalid generated key: %v", err)
		}
		return result.APIKey
	}
	firstKey := createPartner("first")
	secondKey := createPartner("second")
	if firstKey == secondKey {
		t.Fatal("generated keys must be distinct")
	}
	order := `{"order_ref":"shared-order","occurred_at":"2026-01-01T00:00:00Z","currency":"SEK","total_amount":100,"vat_amount":20,"buyer":{"external_id":"buyer","type":"PERSON"},"seller":{"external_id":"seller","type":"ORGANIZATION"},"products":[]}`
	addition := `{"event_ref":"addition","products":[{"total_amount":10,"vat_amount":2,"identifiers":[{"scheme":"serial","value":"first-product"}]}]}`
	if got := request("/partner/v1/orders", firstKey, order); got.Code != http.StatusAccepted {
		t.Fatalf("first partner order: %d %s", got.Code, got.Body)
	}
	if got := request("/partner/v1/orders/shared-order/products", secondKey, addition); got.Code != http.StatusNotFound {
		t.Fatalf("other partner accessed first partner's order: %d %s", got.Code, got.Body)
	}
	if got := request("/partner/v1/orders", secondKey, order); got.Code != http.StatusAccepted {
		t.Fatalf("second partner order: %d %s", got.Code, got.Body)
	}
	if got := request("/partner/v1/orders/shared-order/products", firstKey, addition); got.Code != http.StatusAccepted {
		t.Fatalf("first partner product addition: %d %s", got.Code, got.Body)
	}
	if got := request("/partner/v1/orders", "configured-key", order); got.Code != http.StatusAccepted {
		t.Fatalf("configured fallback: %d %s", got.Code, got.Body)
	}
	if got := request("/partner/v1/orders", "wrong-key", order); got.Code != http.StatusUnauthorized {
		t.Fatalf("invalid key: %d %s", got.Code, got.Body)
	}
	var count int
	if err := db.QueryRow(`SELECT count(DISTINCT partner_id) FROM purchase_events WHERE order_ref = 'shared-order'`).Scan(&count); err != nil || count != 3 {
		t.Fatalf("partner identities not persisted separately: count %d, error %v", count, err)
	}
	if _, err := db.Exec(`INSERT INTO partners (name, partner_id, api_key) VALUES ('duplicate', 'duplicate', $1)`, firstKey); err != nil {
		t.Fatal(err)
	}
	if got := request("/partner/v1/orders", firstKey, order); got.Code != http.StatusServiceUnavailable {
		t.Fatalf("ambiguous key: %d %s", got.Code, got.Body)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if got := request("/partner/v1/orders", secondKey, order); got.Code != http.StatusServiceUnavailable {
		t.Fatalf("database unavailable: %d %s", got.Code, got.Body)
	}
}
