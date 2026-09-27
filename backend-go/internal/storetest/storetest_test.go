package storetest

import (
	"context"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

var validSchemaName = regexp.MustCompile(`^storetest_[a-z0-9_]+_[0-9a-f]{16}$`)

func TestSchemaName(t *testing.T) {
	names := []string{
		"TestSimple",
		"TestParent/sub test with spaces & symbols!",
		"Test" + strings.Repeat("VeryLongName", 20),
		"___",
		"Тест", // no ASCII letters at all
	}
	seen := map[string]bool{}
	for _, testName := range names {
		name, err := schemaName(testName)
		if err != nil {
			t.Fatal(err)
		}
		if !validSchemaName.MatchString(name) || len(name) > 63 || strings.Contains(name, "__") {
			t.Errorf("schemaName(%q) = %q: not a valid, bounded, unquoted identifier", testName, name)
		}
		if seen[name] {
			t.Errorf("schemaName(%q) repeated %q", testName, name)
		}
		seen[name] = true
	}

	a, _ := schemaName("TestSame")
	b, _ := schemaName("TestSame")
	if a == b {
		t.Errorf("two schemas for the same test share the name %q", a)
	}
	if !strings.HasPrefix(a, SchemaPrefix+"testsame_") {
		t.Errorf("schema name %q does not carry the test name", a)
	}
}

func TestWithSearchPath(t *testing.T) {
	cases := []struct{ name, in string }{
		{"url", "postgres://user:pw@localhost:5432/db?sslmode=disable"},
		{"url with socket host", "postgres://postgres@/truthtable_test?host=/run/user/1000/pg&sslmode=disable"},
		{"url overriding search_path", "postgresql://u@h/db?search_path=public"},
		{"keyword/value", "host=localhost dbname=db user=u sslmode=disable"},
		{"keyword/value overriding search_path", "host=localhost dbname=db search_path=public "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := withSearchPath(tc.in, "storetest_x_0123456789abcdef")
			if err != nil {
				t.Fatal(err)
			}
			cfg, err := pgx.ParseConfig(out)
			if err != nil {
				t.Fatalf("result %q does not parse: %v", out, err)
			}
			if got := cfg.RuntimeParams["search_path"]; got != "storetest_x_0123456789abcdef" {
				t.Errorf("search_path = %q in %q", got, out)
			}
			orig, err := pgx.ParseConfig(tc.in)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Host != orig.Host || cfg.Database != orig.Database || cfg.User != orig.User {
				t.Errorf("connection target changed: %q -> %q", tc.in, out)
			}
		})
	}
}

func TestNewSkipsWithoutDatabase(t *testing.T) {
	reached := false
	t.Run("unset", func(t *testing.T) {
		t.Setenv(EnvVar, "")
		New(t)
		reached = true
	})
	if reached {
		t.Fatal("New must skip the test when TEST_DATABASE_URL is unset")
	}
}

func TestNewIsolatesAndDropsSchema(t *testing.T) {
	if os.Getenv(EnvVar) == "" {
		t.Skipf("%s not set", EnvVar)
	}
	ctx := context.Background()

	var names []string
	t.Run("isolated", func(t *testing.T) {
		a, b := New(t), New(t)
		names = []string{a.Name, b.Name}
		poolA, poolB := a.Pool(t), b.Pool(t)

		var current string
		if err := poolA.QueryRow(ctx, `SELECT current_schema()`).Scan(&current); err != nil {
			t.Fatal(err)
		}
		if current != a.Name {
			t.Fatalf("current_schema() = %q, want %q", current, a.Name)
		}

		if _, err := poolA.Exec(ctx, `CREATE TABLE widgets (id INT); INSERT INTO widgets VALUES (1)`); err != nil {
			t.Fatal(err)
		}
		var visible bool
		if err := poolB.QueryRow(ctx, `SELECT to_regclass('widgets') IS NOT NULL`).Scan(&visible); err != nil {
			t.Fatal(err)
		}
		if visible {
			t.Error("a table created in one test schema is visible from another")
		}
	})

	conn, err := pgx.Connect(ctx, os.Getenv(EnvVar))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx) //nolint:errcheck
	var left int
	if err := conn.QueryRow(ctx,
		`SELECT count(*) FROM pg_namespace WHERE nspname = ANY($1)`, names).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 || left != 0 {
		t.Errorf("schemas %v not dropped after the test (%d left)", names, left)
	}
}
