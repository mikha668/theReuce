package migrate

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

func TestProvenanceMigrationPreservesLegacyRows(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	admin, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatal(err)
	}
	schema := "test_" + hex.EncodeToString(suffix)
	if _, err := admin.Exec(`CREATE SCHEMA "` + schema + `"`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := admin.Exec(`DROP SCHEMA "` + schema + `" CASCADE`); err != nil {
			t.Error(err)
		}
	}()
	config, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	config.RuntimeParams["search_path"] = schema
	db := stdlib.OpenDB(*config)
	defer db.Close()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `CREATE TABLE schema_migrations (version TEXT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"0001_init.sql", "0002_frontend_admin.sql", "0003_party_profiles.sql", "0004_independent_party_aliases.sql", "0005_order_amounts.sql"} {
		migration, err := files.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, string(migration)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, name); err != nil {
			t.Fatal(err)
		}
	}
	var productID string
	if err := db.QueryRowContext(ctx, `INSERT INTO product_instances (model) VALUES ('legacy') RETURNING id::text`).Scan(&productID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO product_identifiers (product_instance_id, scheme, value) VALUES ($1, 'serial', 'legacy')`, productID); err != nil {
		t.Fatal(err)
	}
	if err := Up(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := Up(ctx, db); err != nil {
		t.Fatalf("reapply migrations: %v", err)
	}
	var model string
	var actor, correlation sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT model, registered_by_partner_id, registration_correlation_id FROM product_instances WHERE id = $1`, productID).Scan(&model, &actor, &correlation); err != nil {
		t.Fatal(err)
	}
	if model != "legacy" || actor.Valid || correlation.Valid {
		t.Fatalf("legacy product changed or provenance invented: %q, %#v, %#v", model, actor, correlation)
	}
	if err := db.QueryRowContext(ctx, `SELECT registered_by_partner_id, registration_correlation_id FROM product_identifiers WHERE product_instance_id = $1`, productID).Scan(&actor, &correlation); err != nil {
		t.Fatal(err)
	}
	if actor.Valid || correlation.Valid {
		t.Fatal("legacy identifier has invented provenance")
	}
}
