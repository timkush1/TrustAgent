package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrSchemaBehind is returned by OpenPostgresStore when the database lacks migrations this build
// embeds.
var ErrSchemaBehind = errors.New("store: the database schema is behind this build")

// OpenPostgresStore connects to a database that is already migrated, without migrating it. Tools
// that only read (cmd/export-replay) use it so that pointing them at a database never changes its
// schema: it fails with ErrSchemaBehind, naming the missing files, unless every embedded migration
// has been applied (by the proxy or NewPostgresStore).
func OpenPostgresStore(ctx context.Context, databaseURL string) (*PostgresStore, error) {
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
	if err := checkMigrated(ctx, pool); err != nil {
		pool.Close()
		return nil, err
	}
	return &PostgresStore{pool: pool}, nil
}

// checkMigrated reports ErrSchemaBehind unless schema_migrations lists every embedded migration.
func checkMigrated(ctx context.Context, pool *pgxpool.Pool) error {
	migrations, err := loadMigrations()
	if err != nil {
		return fmt.Errorf("load migrations: %w", err)
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	applied, err := appliedMigrations(ctx, conn)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "42P01" { // undefined_table: never migrated
		applied, err = map[string]bool{}, nil
	}
	if err != nil {
		return err
	}
	var missing []string
	for _, m := range migrations {
		if !applied[m.name] {
			missing = append(missing, m.name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: %s not applied (start the proxy once to migrate)", ErrSchemaBehind, strings.Join(missing, ", "))
	}
	return nil
}
