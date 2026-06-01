package api_test

// S3AUDIT-04 integration test — proves the SetActor + audit.Record
// flow lands an ActorKindS3Key actor in audit_log.actor_s3_key_id
// (NOT NULL) with actor_user_id and actor_api_key_id NULL.
//
// Scope: this exercises the column-surfacing flow end-to-end at the
// helper boundary. Wiring audit emission into the live S3 protocol PUT
// path (so that an aws-sdk-go SigV4 PUT writes a real audit_log row) is
// a separate v1.8+ deliverable — the S3 protocol layer
// (`internal/protocol/s3/`) does not currently emit audit events. v1.7
// Phase 3 ships the column + helper extension; full protocol-side
// emission is a feature, not a bug fix.

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/vladoportos/omnirepo/internal/api"
	"github.com/vladoportos/omnirepo/internal/audit"
	"github.com/vladoportos/omnirepo/internal/auth"
	"github.com/vladoportos/omnirepo/internal/metadata/sqlitetest"
)

func TestSetActor_S3Key_AuditLogColumnPopulated(t *testing.T) {
	db := sqlitetest.New(t)
	ctx := context.Background()
	dir := t.TempDir()
	logger, err := audit.New(db, filepath.Join(dir, "audit.log"), 10, 1)
	if err != nil {
		t.Fatalf("audit.New: %v", err)
	}

	// Seed project + user + s3_access_keys so the FK on actor_s3_key_id
	// holds. Mirrors the m037 migration test seed shape.
	if _, err := db.Writer.ExecContext(ctx,
		`INSERT INTO projects(name) VALUES('audit-s3')`); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	var projID int64
	if err := db.Reader.QueryRowContext(ctx,
		`SELECT id FROM projects WHERE name='audit-s3'`).Scan(&projID); err != nil {
		t.Fatalf("scan project id: %v", err)
	}
	if _, err := db.Writer.ExecContext(ctx, `
		INSERT INTO users(login, email, password_hash) VALUES('s3-audit-u', 's3@example', 'h')
	`); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	var userID int64
	if err := db.Reader.QueryRowContext(ctx,
		`SELECT id FROM users WHERE login='s3-audit-u'`).Scan(&userID); err != nil {
		t.Fatalf("scan user id: %v", err)
	}
	if _, err := db.Writer.ExecContext(ctx, `
		INSERT INTO s3_access_keys(project_id, access_key_id, secret_enc, label, created_by_user_id)
		VALUES (?, 'AKIATESTAUDIT', x'00', 'test-key', ?)
	`, projID, userID); err != nil {
		t.Fatalf("seed s3_access_keys: %v", err)
	}
	var s3KeyID int64
	if err := db.Reader.QueryRowContext(ctx,
		`SELECT id FROM s3_access_keys WHERE access_key_id='AKIATESTAUDIT'`).Scan(&s3KeyID); err != nil {
		t.Fatalf("scan s3 key id: %v", err)
	}

	// Drive the canonical flow: SetActor maps an ActorKindS3Key actor
	// into Event.ActorS3KeyID; logger.Record persists it.
	actor := auth.Actor{Kind: auth.ActorKindS3Key, S3KeyID: &s3KeyID}
	e := audit.Event{
		Kind:       audit.EvtS3BucketCreated,
		TargetKind: "s3_bucket",
		TargetID:   "audit-s3/test-bucket",
		Outcome:    "ok",
	}
	api.SetActor(&e, actor)
	if err := logger.Record(ctx, e); err != nil {
		t.Fatalf("logger.Record: %v", err)
	}

	// Assert the row shape: actor_s3_key_id IS NOT NULL, the other two
	// actor columns IS NULL.
	var (
		uid sql.NullInt64
		kid sql.NullInt64
		s3k sql.NullInt64
	)
	if err := db.Reader.QueryRowContext(ctx, `
		SELECT actor_user_id, actor_api_key_id, actor_s3_key_id
		  FROM audit_log
		 WHERE event_kind = ? AND target_id = ?
	`, string(audit.EvtS3BucketCreated), "audit-s3/test-bucket").Scan(&uid, &kid, &s3k); err != nil {
		t.Fatalf("read audit_log: %v", err)
	}
	if uid.Valid {
		t.Fatalf("actor_user_id = %d, want NULL for ActorKindS3Key actor", uid.Int64)
	}
	if kid.Valid {
		t.Fatalf("actor_api_key_id = %d, want NULL for ActorKindS3Key actor", kid.Int64)
	}
	if !s3k.Valid {
		t.Fatal("actor_s3_key_id IS NULL, want NOT NULL after SetActor with ActorKindS3Key")
	}
	if s3k.Int64 != s3KeyID {
		t.Fatalf("actor_s3_key_id = %d, want %d", s3k.Int64, s3KeyID)
	}
}

// TestSetActor_NonS3Actor_LeavesS3KeyColumnNull is the regression guard:
// adding the new column must not corrupt the existing actor flows.
// Driving a session-user actor through the full Record path must yield
// actor_user_id NOT NULL + actor_s3_key_id NULL.
func TestSetActor_NonS3Actor_LeavesS3KeyColumnNull(t *testing.T) {
	db := sqlitetest.New(t)
	ctx := context.Background()
	dir := t.TempDir()
	logger, err := audit.New(db, filepath.Join(dir, "audit.log"), 10, 1)
	if err != nil {
		t.Fatalf("audit.New: %v", err)
	}
	if _, err := db.Writer.ExecContext(ctx, `
		INSERT INTO users(login, email, password_hash) VALUES('user-actor', 'u@x', 'h')
	`); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	var userID int64
	if err := db.Reader.QueryRowContext(ctx,
		`SELECT id FROM users WHERE login='user-actor'`).Scan(&userID); err != nil {
		t.Fatalf("scan user id: %v", err)
	}

	actor := auth.Actor{Kind: auth.ActorKindUser, ID: userID}
	e := audit.Event{
		Kind:       audit.EvtAuthLoginSuccess,
		TargetKind: "user",
		TargetID:   "user-actor",
		Outcome:    "ok",
	}
	api.SetActor(&e, actor)
	if err := logger.Record(ctx, e); err != nil {
		t.Fatalf("logger.Record: %v", err)
	}

	var (
		uid sql.NullInt64
		s3k sql.NullInt64
	)
	if err := db.Reader.QueryRowContext(ctx, `
		SELECT actor_user_id, actor_s3_key_id
		  FROM audit_log
		 WHERE event_kind = ? AND target_id = ?
	`, string(audit.EvtAuthLoginSuccess), "user-actor").Scan(&uid, &s3k); err != nil {
		t.Fatalf("read audit_log: %v", err)
	}
	if !uid.Valid || uid.Int64 != userID {
		t.Fatalf("actor_user_id = %v, want %d", uid, userID)
	}
	if s3k.Valid {
		t.Fatalf("actor_s3_key_id = %d, want NULL for non-S3 actor (regression guard)", s3k.Int64)
	}
}
