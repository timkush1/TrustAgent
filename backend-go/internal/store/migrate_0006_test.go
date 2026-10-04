package store

import (
	"context"
	"strings"
	"testing"

	"github.com/truthtable/backend-go/internal/storetest"
)

// 0006 closes the gap 0005's row trigger leaves: TRUNCATE admin_audit_log is refused as well, the
// rows stay, and running the migrations again keeps exactly one such trigger.
func TestMigration0006RefusesTruncate(t *testing.T) {
	ctx := context.Background()
	pool := storetest.New(t).Pool(t)
	for range 2 {
		if err := Migrate(ctx, pool); err != nil {
			t.Fatalf("Migrate: %v", err)
		}
	}
	runStatementCases(t, ctx, pool, []statementCase{
		{"admin audit entry", `INSERT INTO admin_audit_log (actor, action, target, diff)
			VALUES ('setup-admin', 'settings.update', 'judge', '{}')`, sqlOK},
		{"admin audit truncate", `TRUNCATE admin_audit_log`, sqlRaiseException},
	})
	_, err := pool.Exec(ctx, `TRUNCATE admin_audit_log`)
	if err == nil || !strings.Contains(err.Error(), appendOnlyMessage) {
		t.Errorf("TRUNCATE admin_audit_log error = %v, want %q", err, appendOnlyMessage)
	}
	var rows, triggers int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM admin_audit_log`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_trigger WHERE tgname = 'admin_audit_log_no_truncate' AND tgrelid = 'admin_audit_log'::regclass`).Scan(&triggers); err != nil {
		t.Fatal(err)
	}
	if rows != 1 || triggers != 1 {
		t.Errorf("after the refused TRUNCATE: %d rows, %d truncate triggers; want 1 and 1", rows, triggers)
	}
}
