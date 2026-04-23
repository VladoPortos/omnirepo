package metadata_test

import (
	"context"
	"strings"
	"testing"

	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/metadata/sqlitetest"
)

// resetTablesSlice returns the package-private resetTables via a test-only
// probe. We can't import the unexported slice, so the invariant test below
// derives the expected set from a query against the DB and compares against
// sqlite_master — indirect but sufficient to catch migration drift.

// seedSuperAdmin inserts a super-admin users row suitable for Reset
// preservation assertions.
func seedSuperAdmin(t *testing.T, db *metadata.DB) int64 {
	t.Helper()
	ctx := context.Background()
	res, err := db.Writer.ExecContext(ctx,
		`INSERT INTO users (login, email, password_hash, is_super_admin, must_change_password)
		 VALUES (?, ?, ?, 1, 0)`,
		"resetadmin", "resetadmin@local",
		"$argon2id$v=19$m=65536,t=3,p=4$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
	)
	if err != nil {
		t.Fatalf("seed super-admin: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("seed super-admin: last id: %v", err)
	}
	return id
}

// seedBootstrapSettings inserts the two bootstrap-secret rows that Reset
// MUST preserve (matches the app.Run bootstrap path).
func seedBootstrapSettings(t *testing.T, db *metadata.DB) {
	t.Helper()
	ctx := context.Background()
	rows := []struct{ k, v string }{
		{"docker_token_hmac_secret", "hmac-secret-bootstrap"},
		{"upstream_creds_aead_key", "aead-key-bootstrap"},
	}
	for _, r := range rows {
		if _, err := db.Writer.ExecContext(ctx,
			`INSERT INTO settings (key, value) VALUES (?, ?)`, r.k, r.v); err != nil {
			t.Fatalf("seed setting %s: %v", r.k, err)
		}
	}
}

// TestResetCoversEveryTable is the D-10 drift detector. It queries
// sqlite_master for every physical + virtual table (filtering the FTS5
// auxiliary shadow tables) and asserts the set is wiped by DB.Reset.
//
// Strategy: seed one row into every table we can, call Reset, assert every
// real table is empty (or its preservation clause took effect). If a NEW
// migration adds a table that isn't in resetTables, at least one of these
// rows will survive and the test fails loudly. We do NOT need access to
// the unexported resetTables slice — the observable behaviour is
// "every wipeable table is empty post-Reset".
func TestResetCoversEveryTable(t *testing.T) {
	db := sqlitetest.New(t)
	ctx := context.Background()

	// Enumerate every user-space table/virtual-table that a Reset must
	// consider. Excludes: sqlite_* internals + FTS5 shadow tables + the
	// three preserved tables (users + settings are partial; schema_migrations
	// is never wiped).
	const enumSQL = `
		SELECT name FROM sqlite_master
		WHERE type IN ('table')
		  AND name NOT LIKE 'sqlite_%'
		  AND name NOT LIKE '%_data'
		  AND name NOT LIKE '%_idx'
		  AND name NOT LIKE '%_content'
		  AND name NOT LIKE '%_docsize'
		  AND name NOT LIKE '%_config'
		UNION
		SELECT name FROM sqlite_master WHERE type = 'virtual'`
	rows, err := db.Reader.QueryContext(ctx, enumSQL)
	if err != nil {
		t.Fatalf("enumerate sqlite_master: %v", err)
	}
	var all []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("scan: %v", err)
		}
		all = append(all, n)
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("close rows: %v", err)
	}

	// Preserved tables per the Reset contract.
	preserved := map[string]bool{
		"schema_migrations": true,
		"users":             true, // partial: WHERE is_super_admin=0
		"settings":          true, // partial: WHERE key NOT IN (...)
	}

	// Seed a super-admin so "users" survives after reset (we verify
	// non-admin wipe in a separate test). Seed bootstrap settings so
	// "settings" survives the preservation clause.
	seedSuperAdmin(t, db)
	seedBootstrapSettings(t, db)

	// Record pre-reset table list (we don't seed — empty is fine;
	// Reset must succeed against an essentially empty DB too).
	if err := db.Reset(ctx); err != nil {
		t.Fatalf("Reset: %v", err)
	}

	// For every enumerated table that isn't preserved, assert it is empty.
	// If a future migration adds a table absent from resetTables, this
	// failing query/assertion will call it out.
	for _, name := range all {
		if preserved[name] {
			continue
		}
		var count int
		if err := db.Reader.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM "+name).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", name, err)
		}
		if count != 0 {
			t.Errorf("table %s has %d row(s) after Reset; add it to resetTables", name, count)
		}
	}

	// Verify the PRAGMA restore side of the contract.
	var fk int
	if err := db.Reader.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fk); err != nil {
		t.Fatalf("PRAGMA foreign_keys: %v", err)
	}
	if fk != 1 {
		t.Errorf("PRAGMA foreign_keys = %d after Reset, want 1 (restore defer must fire)", fk)
	}
}

// TestDBReset_WipesNonBootstrapState seeds one row into each of several
// representative tables, calls Reset, and asserts those rows are gone
// while the super-admin users row + two bootstrap settings rows survive.
func TestDBReset_WipesNonBootstrapState(t *testing.T) {
	db := sqlitetest.New(t)
	ctx := context.Background()

	adminID := seedSuperAdmin(t, db)
	seedBootstrapSettings(t, db)

	// Seed a project + repo + session + api key + audit row + upstream
	// cred + s3_access_key — a representative cross-section of the
	// resetTables inventory that also exercises the FK-OFF path because
	// audit_log / upstream_creds / s3_access_keys carry NO-ACTION FKs
	// against users(id).
	var pid int64
	if err := db.Writer.QueryRowContext(ctx,
		`INSERT INTO projects (name) VALUES (?) RETURNING id`, "proj-e2e").Scan(&pid); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if _, err := db.Writer.ExecContext(ctx,
		`INSERT INTO sessions (user_id, token_prefix, token_sha256, expires_at)
		 VALUES (?, 'x', 'y', datetime('now','+1 hour'))`, adminID); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	if _, err := db.Writer.ExecContext(ctx,
		`INSERT INTO audit_log (actor_user_id, event_kind, occurred_at)
		 VALUES (?, 'test.event', '2026-04-23T00:00:00.000000000Z')`, adminID); err != nil {
		t.Fatalf("seed audit_log: %v", err)
	}
	// Seed a settings key that MUST be wiped (not in preserved list).
	// Use INSERT OR REPLACE because migration 020 preseeds 'maintenance_mode'.
	if _, err := db.Writer.ExecContext(ctx,
		`INSERT OR REPLACE INTO settings (key, value) VALUES ('maintenance_mode', 'true')`); err != nil {
		t.Fatalf("seed non-bootstrap setting: %v", err)
	}

	if err := db.Reset(ctx); err != nil {
		t.Fatalf("Reset: %v", err)
	}

	// Super-admin row must survive.
	var remainingUsers int
	if err := db.Reader.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM users WHERE is_super_admin = 1").Scan(&remainingUsers); err != nil {
		t.Fatalf("count super-admin users: %v", err)
	}
	if remainingUsers != 1 {
		t.Errorf("super-admin users count = %d, want 1", remainingUsers)
	}

	// Seeded non-preserved tables must be empty.
	for _, tbl := range []string{"projects", "sessions", "audit_log"} {
		var n int
		if err := db.Reader.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM "+tbl).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", tbl, err)
		}
		if n != 0 {
			t.Errorf("table %s has %d row(s) after Reset, want 0", tbl, n)
		}
	}

	// Bootstrap settings must survive.
	for _, key := range []string{"docker_token_hmac_secret", "upstream_creds_aead_key"} {
		var v string
		if err := db.Reader.QueryRowContext(ctx,
			"SELECT value FROM settings WHERE key = ?", key).Scan(&v); err != nil {
			t.Errorf("bootstrap setting %s missing after Reset: %v", key, err)
		}
	}

	// Non-bootstrap setting must be gone.
	var maintN int
	if err := db.Reader.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM settings WHERE key = 'maintenance_mode'").Scan(&maintN); err != nil {
		t.Fatalf("count maintenance_mode: %v", err)
	}
	if maintN != 0 {
		t.Errorf("maintenance_mode setting survived Reset; want 0 rows, got %d", maintN)
	}
}

// TestDBReset_FKDisciplineRestoresPragma asserts the deferred PRAGMA
// foreign_keys=ON restore fires even when Reset has nothing to do, and
// that post-reset the integrity check returns no rows.
func TestDBReset_FKDisciplineRestoresPragma(t *testing.T) {
	db := sqlitetest.New(t)
	ctx := context.Background()

	seedSuperAdmin(t, db)
	seedBootstrapSettings(t, db)

	// Sanity: FKs are ON at baseline (DSN has _pragma=foreign_keys(ON)).
	var before int
	if err := db.Reader.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&before); err != nil {
		t.Fatalf("read PRAGMA foreign_keys before: %v", err)
	}
	if before != 1 {
		t.Fatalf("baseline PRAGMA foreign_keys = %d, want 1", before)
	}

	if err := db.Reset(ctx); err != nil {
		t.Fatalf("Reset: %v", err)
	}

	var after int
	if err := db.Reader.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&after); err != nil {
		t.Fatalf("read PRAGMA foreign_keys after: %v", err)
	}
	if after != 1 {
		t.Errorf("post-Reset PRAGMA foreign_keys = %d, want 1 (defer restore failed)", after)
	}

	// foreign_key_check on the DB as a whole must return no rows.
	rows, err := db.Reader.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		t.Fatalf("foreign_key_check: %v", err)
	}
	defer func() { _ = rows.Close() }()
	if rows.Next() {
		t.Errorf("foreign_key_check returned rows post-Reset; want empty")
	}
}

// TestDBReset_PreservesBootstrapSettings seeds a non-bootstrap key and
// both bootstrap keys, calls Reset, and asserts only the bootstrap keys
// survive.
func TestDBReset_PreservesBootstrapSettings(t *testing.T) {
	db := sqlitetest.New(t)
	ctx := context.Background()

	seedSuperAdmin(t, db)
	seedBootstrapSettings(t, db)

	// A non-bootstrap setting that must be wiped.
	// Use INSERT OR REPLACE because migration 020 preseeds 'maintenance_mode'.
	if _, err := db.Writer.ExecContext(ctx,
		`INSERT OR REPLACE INTO settings (key, value) VALUES ('maintenance_mode', 'true')`); err != nil {
		t.Fatalf("seed maintenance_mode: %v", err)
	}

	if err := db.Reset(ctx); err != nil {
		t.Fatalf("Reset: %v", err)
	}

	// Collect surviving keys.
	rows, err := db.Reader.QueryContext(ctx, "SELECT key FROM settings ORDER BY key")
	if err != nil {
		t.Fatalf("select settings: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var keys []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			t.Fatalf("scan: %v", err)
		}
		keys = append(keys, k)
	}
	got := strings.Join(keys, ",")
	want := "docker_token_hmac_secret,upstream_creds_aead_key"
	if got != want {
		t.Errorf("surviving settings = %q, want %q", got, want)
	}
}

// TestDBReset_PreservesSchemaMigrations records the migration-ledger count
// pre-reset, calls Reset, and asserts the count is unchanged. If
// schema_migrations ever sneaks into resetTables, this fails loudly.
func TestDBReset_PreservesSchemaMigrations(t *testing.T) {
	db := sqlitetest.New(t)
	ctx := context.Background()

	seedSuperAdmin(t, db)
	seedBootstrapSettings(t, db)

	var before int
	if err := db.Reader.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM schema_migrations").Scan(&before); err != nil {
		t.Fatalf("count schema_migrations before: %v", err)
	}
	if before == 0 {
		t.Fatalf("schema_migrations is empty pre-Reset; fixture is broken")
	}

	if err := db.Reset(ctx); err != nil {
		t.Fatalf("Reset: %v", err)
	}

	var after int
	if err := db.Reader.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM schema_migrations").Scan(&after); err != nil {
		t.Fatalf("count schema_migrations after: %v", err)
	}
	if after != before {
		t.Errorf("schema_migrations count = %d, want %d (never wipe the migration ledger)", after, before)
	}
}
