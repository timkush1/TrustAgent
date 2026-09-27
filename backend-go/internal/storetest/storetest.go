// Package storetest isolates Postgres-backed tests. Each test gets a freshly
// created schema on TEST_DATABASE_URL, selected for every connection through
// the search_path startup parameter, and the schema is dropped (CASCADE) when
// the test ends. Tests skip cleanly when TEST_DATABASE_URL is unset.
//
// Because unqualified names resolve through search_path, code under test
// (migrations included) runs unchanged inside the schema, and tests can run in
// parallel with each other and with other users of the same database.
//
//	s := storetest.New(t)
//	st, err := store.NewPostgresStore(ctx, s.URL) // migrates into s.Name
//
// Schemas are named storetest_<test>_<random>; a crashed run can leave some
// behind, and they are safe to drop by that prefix.
package storetest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// EnvVar names the variable that holds the connection string of the test
// database (URL or keyword/value form).
const EnvVar = "TEST_DATABASE_URL"

// SchemaPrefix starts the name of every schema this package creates.
const SchemaPrefix = "storetest_"

// maxTestNamePart bounds the test-name part of a schema name so the whole name
// stays within Postgres' 63-byte identifier limit.
const maxTestNamePart = 30

// Schema is one isolated per-test schema.
type Schema struct {
	// Name is the schema name (lowercase letters, digits and underscores).
	Name string
	// URL is TEST_DATABASE_URL with search_path pinned to Name. Anything that
	// connects with it creates and reads unqualified tables inside the schema.
	URL string
}

// New creates a fresh schema for t and registers a cleanup that drops it. It
// skips t when TEST_DATABASE_URL is unset and fails t when the database is
// unreachable.
func New(t testing.TB) *Schema {
	t.Helper()
	base := os.Getenv(EnvVar)
	if base == "" {
		t.Skipf("%s not set; skipping Postgres integration test", EnvVar)
	}

	name, err := schemaName(t.Name())
	if err != nil {
		t.Fatalf("storetest: %v", err)
	}
	scoped, err := withSearchPath(base, name)
	if err != nil {
		t.Fatalf("storetest: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := execAdmin(ctx, base, "CREATE SCHEMA "+pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatalf("storetest: create schema %s: %v", name, err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := execAdmin(ctx, base, "DROP SCHEMA IF EXISTS "+pgx.Identifier{name}.Sanitize()+" CASCADE"); err != nil {
			t.Errorf("storetest: drop schema %s: %v", name, err)
		}
	})
	return &Schema{Name: name, URL: scoped}
}

// Pool opens a connection pool bound to the schema. It is closed during
// cleanup, before the schema is dropped.
func (s *Schema) Pool(t testing.TB) *pgxpool.Pool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, s.URL)
	if err != nil {
		t.Fatalf("storetest: open pool: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Fatalf("storetest: ping: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// execAdmin runs one statement on a short-lived connection that uses the
// unmodified TEST_DATABASE_URL.
func execAdmin(ctx context.Context, connString, sql string) error {
	conn, err := pgx.Connect(ctx, connString)
	if err != nil {
		return fmt.Errorf("connect via %s: %w", EnvVar, err)
	}
	defer conn.Close(context.Background()) //nolint:errcheck // best-effort close of a one-shot connection
	_, err = conn.Exec(ctx, sql)
	return err
}

// schemaName derives a unique, valid, unquoted-safe schema name from a test
// name: storetest_<sanitized test name>_<16 random hex digits>.
func schemaName(testName string) (string, error) {
	var b strings.Builder
	lastUnderscore := false
	for _, r := range strings.ToLower(testName) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastUnderscore = false
		case !lastUnderscore:
			b.WriteByte('_')
			lastUnderscore = true
		}
		if b.Len() >= maxTestNamePart {
			break
		}
	}
	part := strings.Trim(b.String(), "_")
	if part == "" {
		part = "test"
	}

	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("random schema suffix: %w", err)
	}
	return SchemaPrefix + part + "_" + hex.EncodeToString(random[:]), nil
}

// withSearchPath returns connString with the search_path startup parameter
// set to schema, replacing any search_path already present. pgx forwards
// unknown connection-string keys to the server as run-time parameters.
func withSearchPath(connString, schema string) (string, error) {
	if strings.HasPrefix(connString, "postgres://") || strings.HasPrefix(connString, "postgresql://") {
		u, err := url.Parse(connString)
		if err != nil {
			return "", fmt.Errorf("parse %s: %w", EnvVar, err)
		}
		query := u.Query()
		query.Set("search_path", schema)
		u.RawQuery = query.Encode()
		return u.String(), nil
	}
	// Keyword/value form: a later key overrides an earlier one.
	return strings.TrimSpace(connString) + " search_path=" + schema, nil
}
