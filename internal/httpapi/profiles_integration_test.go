package httpapi

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"theregister/internal/migrate"
	"theregister/internal/orders"
	"theregister/internal/store"
)

func openProfileTestDatabase(t *testing.T) *sql.DB {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	admin, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatal(err)
	}
	schema := "test_" + hex.EncodeToString(suffix)
	if _, err := admin.Exec(`CREATE SCHEMA "` + schema + `"`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(`DROP SCHEMA "` + schema + `" CASCADE`); err != nil {
			t.Error(err)
		}
	})
	config, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	config.RuntimeParams["search_path"] = schema
	db := stdlib.OpenDB(*config)
	t.Cleanup(func() { _ = db.Close() })
	if err := migrate.Up(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestListPartyProfilesAliases(t *testing.T) {
	db := openProfileTestDatabase(t)
	if _, err := db.Exec(`
		INSERT INTO party_profiles (name, party_type, display_id) VALUES
		  ('No alias', 'PERSON', 'one'), ('With alias', 'PERSON', 'two');
		INSERT INTO party_profile_aliases (party_profile_id, alias)
		  SELECT id, 'customer' FROM party_profiles WHERE display_id = 'two'`); err != nil {
		t.Fatal(err)
	}

	handler := NewServer(db, nil, "", "", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/party-profiles", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var profiles []struct {
		DisplayID string   `json:"display_id"`
		Aliases   []string `json:"aliases"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &profiles); err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 2 {
		t.Fatalf("got %d profiles, want 2", len(profiles))
	}
	for _, profile := range profiles {
		switch profile.DisplayID {
		case "one":
			if profile.Aliases == nil || len(profile.Aliases) != 0 {
				t.Fatalf("empty aliases must be an array, got %#v", profile.Aliases)
			}
		case "two":
			if len(profile.Aliases) != 1 || profile.Aliases[0] != "customer" {
				t.Fatalf("unexpected aliases: %#v", profile.Aliases)
			}
		default:
			t.Fatalf("unexpected profile %q", profile.DisplayID)
		}
	}
}

func TestCreateAndUpdatePartyProfiles(t *testing.T) {
	db := openProfileTestDatabase(t)
	handler := NewServer(db, nil, "", "", nil, []byte(strings.Repeat("x", 32)))
	request := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		result := httptest.NewRecorder()
		handler.ServeHTTP(result, httptest.NewRequest(method, path, strings.NewReader(body)))
		return result
	}
	created := request(http.MethodPost, "/api/v1/party-profiles", `{"name":"First","display_id":"one","party_type":"PERSON","aliases":["nickname","customer"]}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", created.Code, created.Body)
	}
	var profile struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &profile); err != nil {
		t.Fatal(err)
	}
	list := request(http.MethodGet, "/api/v1/party-profiles", "")
	if !strings.Contains(list.Body.String(), `"nickname"`) || !strings.Contains(list.Body.String(), `"customer"`) {
		t.Fatalf("aliases missing from response: %s", list.Body)
	}
	orderStore := store.New(db, []byte(strings.Repeat("x", 32)))
	for _, partner := range []string{"one", "two"} {
		body := `{"order_ref":"order","occurred_at":"2026-01-01T00:00:00Z","currency":"SEK","total_amount":100,"vat_amount":20,"buyer":{"external_id":"customer","type":"PERSON"},"seller":{"external_id":"seller","type":"ORGANIZATION"},"products":[{"total_amount":100,"vat_amount":20,"identifiers":[{"scheme":"serial","value":"` + partner + `"}]}]}`
		order, hash, err := orders.DecodeRequest([]byte(body))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := orderStore.CreateOrder(context.Background(), partner, order, hash, "test"); err != nil {
			t.Fatal(err)
		}
	}
	emptyOrder, emptyHash, err := orders.DecodeRequest([]byte(`{"order_ref":"empty","occurred_at":"2026-01-01T00:00:00Z","currency":"SEK","total_amount":50,"vat_amount":10,"buyer":{"external_id":"customer","type":"PERSON"},"seller":{"external_id":"seller","type":"ORGANIZATION"},"products":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := orderStore.CreateOrder(context.Background(), "one", emptyOrder, emptyHash, "test"); err != nil {
		t.Fatal(err)
	}
	purchases := request(http.MethodGet, "/api/v1/party/"+profile.ID+"/purchases", "")
	if purchases.Code != http.StatusOK {
		t.Fatalf("purchases: %d %s", purchases.Code, purchases.Body)
	}
	var purchaseRows []json.RawMessage
	if err := json.Unmarshal(purchases.Body.Bytes(), &purchaseRows); err != nil || len(purchaseRows) != 3 || !strings.Contains(purchases.Body.String(), `"total_amount":"50.0000"`) {
		t.Fatalf("expected purchases from both integrations including an order without products, got %d: %s (%v)", len(purchaseRows), purchases.Body, err)
	}
	path := "/api/v1/party-profiles/" + profile.ID
	updated := request(http.MethodPut, path, `{"name":"Updated","display_id":"changed","party_type":"ORGANIZATION","aliases":["customer","new alias"]}`)
	if updated.Code != http.StatusOK {
		t.Fatalf("update: %d %s", updated.Code, updated.Body)
	}
	list = request(http.MethodGet, "/api/v1/party-profiles", "")
	if !strings.Contains(list.Body.String(), `"name":"Updated"`) || !strings.Contains(list.Body.String(), `"new alias"`) || strings.Contains(list.Body.String(), `"nickname"`) {
		t.Fatalf("incorrect update: %s", list.Body)
	}
	other := request(http.MethodPost, "/api/v1/party-profiles", `{"name":"Other","display_id":"other","party_type":"PERSON","aliases":["taken"]}`)
	if other.Code != http.StatusCreated {
		t.Fatalf("create second profile: %d %s", other.Code, other.Body)
	}
	conflict := request(http.MethodPut, path, `{"name":"Must roll back","display_id":"changed","party_type":"PERSON","aliases":["taken"]}`)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("conflicting update: %d %s", conflict.Code, conflict.Body)
	}
	list = request(http.MethodGet, "/api/v1/party-profiles", "")
	if !strings.Contains(list.Body.String(), `"name":"Updated"`) || !strings.Contains(list.Body.String(), `"customer"`) || !strings.Contains(list.Body.String(), `"new alias"`) {
		t.Fatalf("conflict did not roll back: %s", list.Body)
	}
	invalid := request(http.MethodPost, "/api/v1/party-profiles", `{"name":"Invalid","display_id":"invalid","party_type":"PERSON","aliases":[""]}`)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("empty alias: %d %s", invalid.Code, invalid.Body)
	}
	missing := request(http.MethodPut, "/api/v1/party-profiles/not-found", `{"name":"Missing","display_id":"missing","party_type":"PERSON"}`)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing profile: %d %s", missing.Code, missing.Body)
	}
	removed := request(http.MethodPut, path, `{"name":"Updated","display_id":"changed","party_type":"PERSON","aliases":["new alias"]}`)
	if removed.Code != http.StatusOK {
		t.Fatalf("remove alias: %d %s", removed.Code, removed.Body)
	}
	purchases = request(http.MethodGet, "/api/v1/party/"+profile.ID+"/purchases", "")
	if err := json.Unmarshal(purchases.Body.Bytes(), &purchaseRows); err != nil || len(purchaseRows) != 0 {
		t.Fatalf("removed alias must no longer match purchases: %s (%v)", purchases.Body, err)
	}
}

func TestIndependentAliasMigrationPreservesLegacyValues(t *testing.T) {
	db := openProfileTestDatabase(t)
	if _, err := db.Exec(`
		DROP TABLE party_profile_aliases;
		DELETE FROM schema_migrations WHERE version = '0004_independent_party_aliases.sql';
		INSERT INTO party_profiles (name, party_type, display_id) VALUES ('Profile', 'PERSON', 'profile');
		INSERT INTO party_aliases (party_profile_id, partner_id, alias)
		  SELECT id, 'old-scope', 'alias-value' FROM party_profiles WHERE display_id = 'profile'`); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	var alias string
	if err := db.QueryRow(`SELECT alias FROM party_profile_aliases`).Scan(&alias); err != nil || alias != "alias-value" {
		t.Fatalf("migration did not preserve alias: %q, %v", alias, err)
	}
}
