package store

import (
	"context"
	"embed"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// migrationLockID is the pg_advisory_lock key that serializes migration runs
// across every process sharing the database (SPEC §2.3).
const migrationLockID int64 = 7212026

// PostgresStore implements Store on top of a pgx connection pool.
type PostgresStore struct {
	pool *pgxpool.Pool
}

// NewPostgresStore connects, verifies the connection, and applies pending
// migrations (see Migrate).
func NewPostgresStore(ctx context.Context, databaseURL string) (*PostgresStore, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid database URL: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres unreachable: %w", err)
	}

	if err := Migrate(ctx, pool); err != nil {
		pool.Close()
		return nil, fmt.Errorf("migrations failed: %w", err)
	}
	return &PostgresStore{pool: pool}, nil
}

// migration is one embedded *.up.sql file.
type migration struct {
	name string
	sql  string
}

// loadMigrations returns the embedded *.up.sql files in filename order. The
// schema history is linear and forward-only (see docs/DECISIONS.md), so the
// runner is deliberately minimal.
func loadMigrations() ([]migration, error) {
	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".up.sql") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)

	migrations := make([]migration, 0, len(names))
	for _, name := range names {
		sql, err := migrationFiles.ReadFile("migrations/" + name)
		if err != nil {
			return nil, err
		}
		migrations = append(migrations, migration{name: name, sql: string(sql)})
	}
	return migrations, nil
}

// Migrate applies every pending embedded migration, tracked by filename in
// schema_migrations. It is safe to call concurrently from several processes:
//
//  1. pg_advisory_lock(7212026) is taken on a dedicated connection before
//     schema_migrations is created or read, so runners are fully serialized;
//  2. each file runs in one transaction together with its schema_migrations
//     insert, so a failed file leaves neither partial DDL nor a tracking row;
//  3. the lock is released (or its session closed) before returning.
//
// Re-running is a no-op. Migration files must therefore be transaction-safe
// (no CREATE INDEX CONCURRENTLY and the like). Tables are created in the first
// schema on the connection's search_path, which is how storetest isolates tests.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	migrations, err := loadMigrations()
	if err != nil {
		return fmt.Errorf("load migrations: %w", err)
	}
	return applyMigrations(ctx, pool, migrations)
}

func applyMigrations(ctx context.Context, pool *pgxpool.Pool, migrations []migration) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration connection: %w", err)
	}
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, migrationLockID); err != nil {
		// The lock may or may not have been granted before the failure;
		// closing the session is the only way to be sure it is not kept.
		discardConn(conn)
		return fmt.Errorf("take migration lock: %w", err)
	}
	defer releaseMigrationLock(conn)

	if _, err := conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			filename   TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	applied, err := appliedMigrations(ctx, conn)
	if err != nil {
		return err
	}
	for _, m := range migrations {
		if applied[m.name] {
			continue
		}
		if err := applyMigration(ctx, conn, m); err != nil {
			return err
		}
		log.Printf("Applied migration %s", m.name)
	}
	return nil
}

func appliedMigrations(ctx context.Context, conn *pgxpool.Conn) (map[string]bool, error) {
	rows, err := conn.Query(ctx, `SELECT filename FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("read schema_migrations: %w", err)
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("read schema_migrations: %w", err)
	}
	applied := make(map[string]bool, len(names))
	for _, name := range names {
		applied[name] = true
	}
	return applied, nil
}

// applyMigration runs one file and records it in a single transaction.
func applyMigration(ctx context.Context, conn *pgxpool.Conn, m migration) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("applying %s: begin: %w", m.name, err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op

	// No arguments, so pgx uses the simple protocol, which accepts a
	// multi-statement file.
	if _, err := tx.Exec(ctx, m.sql); err != nil {
		return fmt.Errorf("applying %s: %w", m.name, err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO schema_migrations (filename) VALUES ($1)`, m.name); err != nil {
		return fmt.Errorf("recording %s: %w", m.name, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("applying %s: commit: %w", m.name, err)
	}
	return nil
}

// releaseMigrationLock unlocks and returns conn to the pool. It uses its own
// context so a cancelled migration still releases the lock.
func releaseMigrationLock(conn *pgxpool.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var released bool
	err := conn.QueryRow(ctx, `SELECT pg_advisory_unlock($1)`, migrationLockID).Scan(&released)
	if err != nil || !released {
		// A pooled connection that might still hold the lock would block every
		// later migrator forever; ending the session releases it server-side.
		log.Printf("Releasing migration lock failed (released=%v, err=%v); closing connection", released, err)
		discardConn(conn)
		return
	}
	conn.Release()
}

// discardConn removes conn from the pool and closes its session.
func discardConn(conn *pgxpool.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = conn.Hijack().Close(ctx)
}
