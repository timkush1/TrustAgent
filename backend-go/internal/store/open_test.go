package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/truthtable/backend-go/internal/storetest"
)

func TestOpenPostgresStoreNeverMigrates(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	_, err := OpenPostgresStore(ctx, s.URL)
	if !errors.Is(err, ErrSchemaBehind) || !strings.Contains(err.Error(), "0001_create_audits.up.sql") {
		t.Fatalf("empty schema: %v, want ErrSchemaBehind naming the missing files", err)
	}
	var tables int
	if err := s.Pool(t).QueryRow(ctx, `SELECT count(*) FROM information_schema.tables WHERE table_schema = $1`, s.Name).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if tables != 0 {
		t.Fatalf("OpenPostgresStore created %d tables", tables)
	}

	migrated, err := NewPostgresStore(ctx, s.URL)
	if err != nil {
		t.Fatal(err)
	}
	migrated.Close()
	ro, err := OpenPostgresStore(ctx, s.URL)
	if err != nil {
		t.Fatalf("migrated schema: %v", err)
	}
	defer ro.Close()
	if name, err := ro.Migration(ctx); err != nil || !strings.HasSuffix(name, ".up.sql") {
		t.Errorf("Migration() = %q, %v", name, err)
	}

	// One missing file is reported.
	if _, err := s.Pool(t).Exec(ctx, `DELETE FROM schema_migrations WHERE filename = '0004_security_costs.up.sql'`); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenPostgresStore(ctx, s.URL); !errors.Is(err, ErrSchemaBehind) || !strings.Contains(err.Error(), "0004_security_costs.up.sql not applied") {
		t.Errorf("a schema missing 0004: %v", err)
	}
	if _, err := OpenPostgresStore(ctx, "postgres://%zz"); err == nil {
		t.Error("an invalid URL was accepted")
	}
}
