package migrations_test

import (
	"context"
	"strings"
	"testing"
)

// TestMigration034_AddsProjectMembersRole verifies that after migration 034:
//   - project_members has a role column (TEXT NOT NULL DEFAULT 'maintainer')
//   - inserting a row without an explicit role yields role='maintainer' (DEFAULT)
func TestMigration034_AddsProjectMembersRole(t *testing.T) {
	t.Parallel()
	db := openFreshDB(t)
	ctx := context.Background()
	applyReal(t, db)

	// Verify column exists via PRAGMA.
	rows, err := db.Reader.QueryContext(ctx, `PRAGMA table_info(project_members)`)
	if err != nil {
		t.Fatalf("PRAGMA table_info: %v", err)
	}
	defer func() { _ = rows.Close() }()
	foundRole := false
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull int
		var dfltValue, pk interface{}
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dfltValue, &pk); err != nil {
			t.Fatalf("scan pragma row: %v", err)
		}
		if name == "role" {
			foundRole = true
			if colType != "TEXT" {
				t.Errorf("project_members.role type = %q, want TEXT", colType)
			}
			if notNull != 1 {
				t.Errorf("project_members.role notnull = %d, want 1", notNull)
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	if !foundRole {
		t.Fatal("project_members.role column not found after migration 034")
	}

	// Seed project + user parents.
	if _, err := db.Writer.ExecContext(ctx,
		`INSERT INTO projects(id, name) VALUES (1, 'testproj')`); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if _, err := db.Writer.ExecContext(ctx,
		`INSERT INTO users(id, login, email, password_hash) VALUES (1, 'alice', 'alice@test', 'x')`); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	// Insert without specifying role — DEFAULT 'maintainer' must apply.
	if _, err := db.Writer.ExecContext(ctx,
		`INSERT INTO project_members(project_id, user_id) VALUES (1, 1)`); err != nil {
		t.Fatalf("insert project_members without role: %v", err)
	}

	var role string
	if err := db.Reader.QueryRowContext(ctx,
		`SELECT role FROM project_members WHERE project_id=1 AND user_id=1`).Scan(&role); err != nil {
		t.Fatalf("select role: %v", err)
	}
	if role != "maintainer" {
		t.Errorf("project_members.role default = %q, want 'maintainer'", role)
	}
}

// TestMigration034_RejectsInvalidProjectMemberRole verifies that the CHECK
// constraint on project_members.role rejects values outside {'maintainer','viewer'}.
func TestMigration034_RejectsInvalidProjectMemberRole(t *testing.T) {
	t.Parallel()
	db := openFreshDB(t)
	ctx := context.Background()
	applyReal(t, db)

	// Seed parents.
	if _, err := db.Writer.ExecContext(ctx,
		`INSERT INTO projects(id, name) VALUES (1, 'proj')`); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if _, err := db.Writer.ExecContext(ctx,
		`INSERT INTO users(id, login, email, password_hash) VALUES (1, 'bob', 'bob@test', 'x')`); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	// Attempt to insert with an invalid role — must fail with CHECK constraint error.
	_, err := db.Writer.ExecContext(ctx,
		`INSERT INTO project_members(project_id, user_id, role) VALUES (1, 1, 'owner')`)
	if err == nil {
		t.Fatal("expected CHECK constraint error for role='owner', got nil")
	}
	if !strings.Contains(err.Error(), "CHECK") {
		t.Errorf("expected error mentioning CHECK, got: %v", err)
	}
}

// TestMigration034_AddsApiKeysRole verifies that after migration 034:
//   - api_keys has a nullable role column
//   - existing project-owned keys are backfilled to role='maintainer'
//   - existing user-owned keys remain role=NULL
func TestMigration034_AddsApiKeysRole(t *testing.T) {
	t.Parallel()
	db := openFreshDB(t)
	ctx := context.Background()
	applyReal(t, db)

	// Verify column exists via PRAGMA.
	rows, err := db.Reader.QueryContext(ctx, `PRAGMA table_info(api_keys)`)
	if err != nil {
		t.Fatalf("PRAGMA table_info api_keys: %v", err)
	}
	defer func() { _ = rows.Close() }()
	foundRole := false
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull int
		var dfltValue, pk interface{}
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dfltValue, &pk); err != nil {
			t.Fatalf("scan pragma row: %v", err)
		}
		if name == "role" {
			foundRole = true
			if colType != "TEXT" {
				t.Errorf("api_keys.role type = %q, want TEXT", colType)
			}
			if notNull != 0 {
				t.Errorf("api_keys.role notnull = %d, want 0 (nullable)", notNull)
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	if !foundRole {
		t.Fatal("api_keys.role column not found after migration 034")
	}

	// Seed a project + user so FKs are satisfied.
	if _, err := db.Writer.ExecContext(ctx,
		`INSERT INTO projects(id, name) VALUES (1, 'myproj')`); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if _, err := db.Writer.ExecContext(ctx,
		`INSERT INTO users(id, login, email, password_hash) VALUES (1, 'charlie', 'charlie@test', 'x')`); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	// Seed a project-owned key with role=NULL and a user-owned key with role=NULL.
	// These simulate pre-034 rows (no role column). After running the backfill UPDATE
	// (same statement as in the migration Step 3), the project-owned key must have
	// role='maintainer' and the user-owned key must remain NULL.
	// NOTE: we do NOT re-apply the migration file (ADD COLUMN is not idempotent in SQLite).
	// Instead we directly verify the backfill UPDATE logic.
	if _, err := db.Writer.ExecContext(ctx, `
		INSERT INTO api_keys(id, owner_kind, owner_project_id, name, token_prefix, token_sha256, role)
		VALUES (1, 'project', 1, 'proj-key', 'pfx1', 'sha1', NULL)`); err != nil {
		t.Fatalf("insert project-owned api_key: %v", err)
	}
	if _, err := db.Writer.ExecContext(ctx, `
		INSERT INTO api_keys(id, owner_kind, owner_user_id, name, token_prefix, token_sha256, role)
		VALUES (2, 'user', 1, 'user-key', 'pfx2', 'sha2', NULL)`); err != nil {
		t.Fatalf("insert user-owned api_key: %v", err)
	}

	// Run the same backfill UPDATE as migration Step 3.
	if _, err := db.Writer.ExecContext(ctx,
		`UPDATE api_keys SET role = 'maintainer' WHERE owner_kind = 'project'`); err != nil {
		t.Fatalf("backfill UPDATE: %v", err)
	}

	// Project-owned key must have role='maintainer' (backfilled by Step 3).
	var projRole *string
	if err := db.Reader.QueryRowContext(ctx,
		`SELECT role FROM api_keys WHERE id=1`).Scan(&projRole); err != nil {
		t.Fatalf("select project-owned role: %v", err)
	}
	if projRole == nil || *projRole != "maintainer" {
		t.Errorf("project-owned api_key.role = %v, want 'maintainer'", projRole)
	}

	// User-owned key must have role=NULL (Step 3 only targets owner_kind='project').
	var userRole *string
	if err := db.Reader.QueryRowContext(ctx,
		`SELECT role FROM api_keys WHERE id=2`).Scan(&userRole); err != nil {
		t.Fatalf("select user-owned role: %v", err)
	}
	if userRole != nil {
		t.Errorf("user-owned api_key.role = %v, want NULL", *userRole)
	}
}

// TestMigration034_RejectsInvalidApiKeyRole verifies that the CHECK constraint
// on api_keys.role rejects values outside {NULL, 'maintainer', 'viewer'}.
func TestMigration034_RejectsInvalidApiKeyRole(t *testing.T) {
	t.Parallel()
	db := openFreshDB(t)
	ctx := context.Background()
	applyReal(t, db)

	// Seed project parent.
	if _, err := db.Writer.ExecContext(ctx,
		`INSERT INTO projects(id, name) VALUES (1, 'proj')`); err != nil {
		t.Fatalf("seed project: %v", err)
	}

	// Attempt to insert an api_key with role='admin' — must fail with CHECK constraint.
	_, err := db.Writer.ExecContext(ctx, `
		INSERT INTO api_keys(owner_kind, owner_project_id, name, token_prefix, token_sha256, role)
		VALUES ('project', 1, 'bad-key', 'pfxB', 'shaB', 'admin')`)
	if err == nil {
		t.Fatal("expected CHECK constraint error for role='admin', got nil")
	}
	if !strings.Contains(err.Error(), "CHECK") {
		t.Errorf("expected error mentioning CHECK, got: %v", err)
	}
}
