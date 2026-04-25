package migrations_test

import (
	"context"
	"strings"
	"testing"
)

// TestMigration037_FreshApply verifies that on a fresh DB after applying
// every migration through 037:
//   - audit_log.actor_s3_key_id exists, is INTEGER, NULLable
//   - the new index idx_audit_actor_s3 was created
//   - the existing actor_user_id / actor_api_key_id columns are unchanged
//     (regression guard against an over-eager table rebuild)
//
// This is the schema shape that lets api.SetActor populate the column
// for ActorKindS3Key without fabricating user/api-key ids (S3AUDIT-01..02).
func TestMigration037_FreshApply(t *testing.T) {
	t.Parallel()
	db := openFreshDB(t)
	ctx := context.Background()
	applyReal(t, db)

	cols := readM036PragmaTableInfo(t, db, "audit_log")

	s3Col, ok := cols["actor_s3_key_id"]
	if !ok {
		t.Fatal("actor_s3_key_id column missing after migration 037")
	}
	if s3Col.notNull != 0 {
		t.Errorf("actor_s3_key_id notnull = %d, want 0 (NULLable)", s3Col.notNull)
	}
	if !strings.EqualFold(s3Col.colType, "INTEGER") {
		t.Errorf("actor_s3_key_id type = %q, want INTEGER", s3Col.colType)
	}

	// Pre-existing actor columns must still be present and NULLable.
	for _, name := range []string{"actor_user_id", "actor_api_key_id"} {
		col, ok := cols[name]
		if !ok {
			t.Fatalf("regression: %s column missing after migration 037", name)
		}
		if col.notNull != 0 {
			t.Errorf("regression: %s notnull = %d, want 0", name, col.notNull)
		}
	}

	// Confirm the new index exists.
	var idxName string
	row := db.Reader.QueryRowContext(ctx,
		`SELECT name FROM sqlite_master WHERE type='index' AND name='idx_audit_actor_s3'`)
	if err := row.Scan(&idxName); err != nil {
		t.Fatalf("idx_audit_actor_s3 missing after migration 037: %v", err)
	}
}

// TestMigration037_AuditInsertWithS3Key proves the new column accepts a
// non-NULL FK to s3_access_keys and that an INSERT with NULLs in the
// other two actor columns survives. This is the canonical row shape for
// an S3-authenticated state change post-S3AUDIT-02.
func TestMigration037_AuditInsertWithS3Key(t *testing.T) {
	t.Parallel()
	db := openFreshDB(t)
	ctx := context.Background()
	applyReal(t, db)

	// Need a project + user + s3_access_keys row to satisfy the FK.
	if _, err := db.Writer.ExecContext(ctx,
		`INSERT INTO projects(name) VALUES('p1')`); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	var projID int64
	if err := db.Reader.QueryRowContext(ctx,
		`SELECT id FROM projects WHERE name='p1'`).Scan(&projID); err != nil {
		t.Fatalf("scan project id: %v", err)
	}
	if _, err := db.Writer.ExecContext(ctx, `
		INSERT INTO users(login, email, password_hash) VALUES('u1', 'u1@example', 'h')
	`); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	var userID int64
	if err := db.Reader.QueryRowContext(ctx,
		`SELECT id FROM users WHERE login='u1'`).Scan(&userID); err != nil {
		t.Fatalf("scan user id: %v", err)
	}
	if _, err := db.Writer.ExecContext(ctx, `
		INSERT INTO s3_access_keys(project_id, access_key_id, secret_enc, label, created_by_user_id)
		VALUES (?, 'AKIATEST', x'00', 'l', ?)
	`, projID, userID); err != nil {
		t.Fatalf("seed s3_access_keys: %v", err)
	}
	var s3KeyID int64
	if err := db.Reader.QueryRowContext(ctx,
		`SELECT id FROM s3_access_keys WHERE access_key_id='AKIATEST'`).Scan(&s3KeyID); err != nil {
		t.Fatalf("scan s3 key id: %v", err)
	}

	if _, err := db.Writer.ExecContext(ctx, `
		INSERT INTO audit_log(occurred_at, actor_user_id, actor_api_key_id, actor_s3_key_id,
			ip, user_agent, event_kind, target_kind, target_id, outcome, details_json)
		VALUES (?, NULL, NULL, ?, '', '', 's3.put_object', 's3_object', 'p1/bucket/key', 'ok', '{}')
	`, "2026-04-25T00:00:00.000000000Z", s3KeyID); err != nil {
		t.Fatalf("insert audit row: %v", err)
	}

	var got int64
	if err := db.Reader.QueryRowContext(ctx,
		`SELECT actor_s3_key_id FROM audit_log WHERE event_kind='s3.put_object'`).Scan(&got); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got != s3KeyID {
		t.Fatalf("actor_s3_key_id = %d, want %d", got, s3KeyID)
	}

	// Negative: violating the FK (non-existent s3_access_keys.id) must fail
	// when foreign_keys=ON. The runner toggles it back ON after migrations,
	// so a fresh writer-tx connection should enforce it.
	_ = db.Writer.QueryRowContext(ctx, `PRAGMA foreign_keys=ON`).Scan(new(any))
	_, err := db.Writer.ExecContext(ctx, `
		INSERT INTO audit_log(occurred_at, actor_user_id, actor_api_key_id, actor_s3_key_id,
			ip, user_agent, event_kind, target_kind, target_id, outcome, details_json)
		VALUES (?, NULL, NULL, 999999, '', '', 'fk.test', '', '', 'ok', '{}')
	`, "2026-04-25T00:00:00.000000000Z")
	if err == nil {
		t.Fatal("expected FK violation for actor_s3_key_id=999999, got nil")
	}
}
