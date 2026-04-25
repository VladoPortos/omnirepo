package metadata_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/metadata/sqlitetest"
)

func seedProject(t *testing.T, db *metadata.DB, name string) int64 {
	t.Helper()
	var id int64
	err := db.WriteTx(context.Background(), func(tx *sql.Tx) error {
		res, err := tx.ExecContext(context.Background(), `INSERT INTO projects(name) VALUES (?)`, name)
		if err != nil {
			return err
		}
		id, _ = res.LastInsertId()
		return nil
	})
	if err != nil {
		t.Fatalf("seedProject: %v", err)
	}
	return id
}

func TestAPIKeysRepo_CreateUserKeyAndFind(t *testing.T) {
	db := sqlitetest.New(t)
	uid := seedUser(t, db, "alice")
	repo := metadata.NewAPIKeysRepo(db)
	ctx := context.Background()
	id, err := repo.CreateUserKey(ctx, uid, "dev-laptop", "abcdefgh", "sha256hex")
	if err != nil {
		t.Fatalf("CreateUserKey: %v", err)
	}
	k, err := repo.FindByPrefixSha(ctx, "abcdefgh", "sha256hex")
	if err != nil {
		t.Fatalf("FindByPrefixSha: %v", err)
	}
	if k.ID != id || k.OwnerKind != "user" || k.OwnerUserID == nil || *k.OwnerUserID != uid {
		t.Fatalf("unexpected %+v", k)
	}
	if k.OwnerProjectID != nil {
		t.Fatalf("project id should be nil for user key")
	}
}

func TestAPIKeysRepo_CreateProjectKeyAndFind(t *testing.T) {
	db := sqlitetest.New(t)
	pid := seedProject(t, db, "acme")
	repo := metadata.NewAPIKeysRepo(db)
	ctx := context.Background()
	id, err := repo.CreateProjectKey(ctx, pid, "ci", "12345678", "shaP")
	if err != nil {
		t.Fatalf("CreateProjectKey: %v", err)
	}
	k, err := repo.FindByPrefixSha(ctx, "12345678", "shaP")
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if k.ID != id || k.OwnerKind != "project" || k.OwnerProjectID == nil || *k.OwnerProjectID != pid {
		t.Fatalf("unexpected %+v", k)
	}
}

func TestAPIKeysRepo_TouchLastUsed(t *testing.T) {
	db := sqlitetest.New(t)
	uid := seedUser(t, db, "alice")
	repo := metadata.NewAPIKeysRepo(db)
	ctx := context.Background()
	id, err := repo.CreateUserKey(ctx, uid, "k", "abcdefgh", "sha")
	if err != nil {
		t.Fatalf("CreateUserKey: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	if err := repo.TouchLastUsed(ctx, id, now); err != nil {
		t.Fatalf("TouchLastUsed: %v", err)
	}
	k, err := repo.FindByPrefixSha(ctx, "abcdefgh", "sha")
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if k.LastUsedAt == nil || !k.LastUsedAt.Equal(now) {
		t.Fatalf("LastUsedAt: %v, want %v", k.LastUsedAt, now)
	}
}

// TestAPIKeysRepo_RevokeProjectOwnedForProject pins LIFECYCLE-03 cascade.
// Project-owned keys for a project get cascade-revoked; user-owned keys are
// untouched even when their owner is a project member; pre-revoked keys
// keep their original timestamp.
func TestAPIKeysRepo_RevokeProjectOwnedForProject(t *testing.T) {
	db := sqlitetest.New(t)
	uid := seedUser(t, db, "alice")
	pid := seedProject(t, db, "casc-proj")
	repo := metadata.NewAPIKeysRepo(db)
	ctx := context.Background()

	// Two project-owned keys + one user-owned key on the same project's user.
	pk1, err := repo.CreateProjectKey(ctx, pid, "ci-1", "pkpref01", "shaPK1")
	if err != nil {
		t.Fatalf("CreateProjectKey 1: %v", err)
	}
	pk2, err := repo.CreateProjectKey(ctx, pid, "ci-2", "pkpref02", "shaPK2")
	if err != nil {
		t.Fatalf("CreateProjectKey 2: %v", err)
	}
	uk, err := repo.CreateUserKey(ctx, uid, "alice-laptop", "ukpref01", "shaUK1")
	if err != nil {
		t.Fatalf("CreateUserKey: %v", err)
	}
	// Pre-revoke pk2 with a sentinel TS.
	const preTS = "1999-01-01 00:00:00"
	if _, err := db.Writer.ExecContext(ctx,
		`UPDATE api_keys SET revoked_at=? WHERE id=?`, preTS, pk2,
	); err != nil {
		t.Fatalf("pre-revoke: %v", err)
	}

	// Cascade.
	const cascadeTS = "2026-04-25 12:34:56"
	var n int64
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		var cErr error
		n, cErr = repo.RevokeProjectOwnedForProject(ctx, tx, pid, cascadeTS)
		return cErr
	}); err != nil {
		t.Fatalf("cascade: %v", err)
	}
	if n != 1 {
		t.Fatalf("RevokeProjectOwnedForProject rows=%d want 1 (pk1 only)", n)
	}

	// pk1 must now be revoked (cascade-stamped). NOTE: api_keys.revoked_at is
	// TIMESTAMP affinity, so modernc/sqlite normalizes the read-back value to
	// ISO-8601 ("YYYY-MM-DDTHH:MM:SSZ") even though we wrote a "YYYY-MM-DD
	// HH:MM:SS" string. WHERE-clause equality still works (the storage layer
	// compares input bytes against the stored bytes), but read-back assertions
	// must use a WHERE-equality check rather than literal string compare.
	var matchPk1 int
	if err := db.Reader.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM api_keys WHERE id=? AND revoked_at = ?`, pk1, cascadeTS,
	).Scan(&matchPk1); err != nil {
		t.Fatalf("equality probe pk1: %v", err)
	}
	if matchPk1 != 1 {
		t.Fatalf("pk1: cascade timestamp not stored (equality probe got %d)", matchPk1)
	}
	// pk2 must keep preTS (independent revoke survives).
	var matchPk2 int
	_ = db.Reader.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM api_keys WHERE id=? AND revoked_at = ?`, pk2, preTS,
	).Scan(&matchPk2)
	if matchPk2 != 1 {
		t.Fatalf("pk2: pre-revoke timestamp clobbered (equality probe got %d)", matchPk2)
	}
	// User-owned key untouched (still NULL).
	var got sql.NullString
	_ = db.Reader.QueryRowContext(ctx, `SELECT revoked_at FROM api_keys WHERE id=?`, uk).Scan(&got)
	if got.Valid {
		t.Fatalf("uk revoked_at=%q want NULL (owner_kind filter must spare user-owned)", got.String)
	}

	// Idempotency.
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		n2, e2 := repo.RevokeProjectOwnedForProject(ctx, tx, pid, "2099-12-31 23:59:59")
		if e2 != nil {
			return e2
		}
		if n2 != 0 {
			t.Fatalf("idempotent cascade rows=%d want 0", n2)
		}
		return nil
	}); err != nil {
		t.Fatalf("idempotent: %v", err)
	}
}

// TestAPIKeysRepo_RestoreCascadedProjectOwnedForProject pins LIFECYCLE-03
// reverse cascade with timestamp-equality filter.
func TestAPIKeysRepo_RestoreCascadedProjectOwnedForProject(t *testing.T) {
	db := sqlitetest.New(t)
	uid := seedUser(t, db, "alice")
	pid := seedProject(t, db, "casc-restore-proj")
	repo := metadata.NewAPIKeysRepo(db)
	ctx := context.Background()

	pk1, err := repo.CreateProjectKey(ctx, pid, "ci-1", "rkpref01", "shaR1")
	if err != nil {
		t.Fatalf("CreateProjectKey 1: %v", err)
	}
	pk2, err := repo.CreateProjectKey(ctx, pid, "ci-2", "rkpref02", "shaR2")
	if err != nil {
		t.Fatalf("CreateProjectKey 2: %v", err)
	}
	uk, err := repo.CreateUserKey(ctx, uid, "alice-laptop", "ukrpref0", "shaUR1")
	if err != nil {
		t.Fatalf("CreateUserKey: %v", err)
	}
	const preTS = "1999-01-01 00:00:00"
	if _, err := db.Writer.ExecContext(ctx,
		`UPDATE api_keys SET revoked_at=? WHERE id=?`, preTS, pk2,
	); err != nil {
		t.Fatalf("pre-revoke: %v", err)
	}

	const cascadeTS = "2026-04-25 12:34:56"
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := repo.RevokeProjectOwnedForProject(ctx, tx, pid, cascadeTS)
		return err
	}); err != nil {
		t.Fatalf("cascade: %v", err)
	}

	// Reverse cascade: only pk1 should be restored.
	var restored int64
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		var rErr error
		restored, rErr = repo.RestoreCascadedProjectOwnedForProject(ctx, tx, pid, cascadeTS)
		return rErr
	}); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if restored != 1 {
		t.Fatalf("restored=%d want 1", restored)
	}

	var got sql.NullString
	_ = db.Reader.QueryRowContext(ctx, `SELECT revoked_at FROM api_keys WHERE id=?`, pk1).Scan(&got)
	if got.Valid {
		t.Fatalf("pk1 revoked_at=%q want NULL post-restore", got.String)
	}
	// pk2 must still match preTS via WHERE-equality (read-back format may differ).
	var matchPk2 int
	_ = db.Reader.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM api_keys WHERE id=? AND revoked_at = ?`, pk2, preTS,
	).Scan(&matchPk2)
	if matchPk2 != 1 {
		t.Fatalf("pk2: independent revoke must survive Restore (equality probe got %d)", matchPk2)
	}
	_ = db.Reader.QueryRowContext(ctx, `SELECT revoked_at FROM api_keys WHERE id=?`, uk).Scan(&got)
	if got.Valid {
		t.Fatalf("uk should still be NULL: %v", got.String)
	}

	// Restoring with non-matching TS is a no-op.
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		n, rErr := repo.RestoreCascadedProjectOwnedForProject(ctx, tx, pid, "9999-99-99 99:99:99")
		if rErr != nil {
			return rErr
		}
		if n != 0 {
			t.Fatalf("non-match restore rows=%d want 0", n)
		}
		return nil
	}); err != nil {
		t.Fatalf("non-match: %v", err)
	}
}

// -- LIFECYCLE-07 lookup-hardening tests -----------------------------------
//
// All six tests below soft-delete the project via raw UPDATE rather than
// going through ProjectsRepo.SoftDelete — the plan 01-01 cascade also
// revokes project-owned api_keys, masking the lookup-hardening behavior we
// need to test (the LEFT JOIN + conditional WHERE filter).

// rawSoftDeleteProject soft-deletes a project via raw SQL, decoupled from
// plan 01-01 cascade. Used only by lookup-hardening tests.
func rawSoftDeleteProject(t *testing.T, db *metadata.DB, projectID int64) {
	t.Helper()
	if _, err := db.Writer.ExecContext(context.Background(),
		`UPDATE projects SET deleted_at = CURRENT_TIMESTAMP WHERE id = ?`, projectID,
	); err != nil {
		t.Fatalf("raw soft-delete project %d: %v", projectID, err)
	}
}

// TestAPIKeysRepo_FindByPrefixSha_DeletedProject pins LIFECYCLE-07: a
// project-owned API key whose owning project is soft-deleted MUST collapse
// to ErrNotFound.
func TestAPIKeysRepo_FindByPrefixSha_DeletedProject(t *testing.T) {
	db := sqlitetest.New(t)
	pid := seedProject(t, db, "deleted-proj")
	repo := metadata.NewAPIKeysRepo(db)
	ctx := context.Background()

	if _, err := repo.CreateProjectKey(ctx, pid, "ci", "delpref0", "delsha"); err != nil {
		t.Fatalf("CreateProjectKey: %v", err)
	}
	// Sanity: live project resolves.
	if _, err := repo.FindByPrefixSha(ctx, "delpref0", "delsha"); err != nil {
		t.Fatalf("pre-soft-delete: want resolve, got %v", err)
	}

	rawSoftDeleteProject(t, db, pid)

	_, err := repo.FindByPrefixSha(ctx, "delpref0", "delsha")
	if !errors.Is(err, metadata.ErrNotFound) {
		t.Fatalf("post-soft-delete: want ErrNotFound, got %v", err)
	}
}

// TestAPIKeysRepo_FindByPrefixSha_UserOwnedUnaffected pins that user
// lifecycle is out of scope for this phase — user-owned keys MUST still
// resolve even when an unrelated project is soft-deleted (the LEFT JOIN
// filter must whitelist user-owned rows via the OR clause).
func TestAPIKeysRepo_FindByPrefixSha_UserOwnedUnaffected(t *testing.T) {
	db := sqlitetest.New(t)
	uid := seedUser(t, db, "alice")
	pid := seedProject(t, db, "unrelated-proj")
	repo := metadata.NewAPIKeysRepo(db)
	ctx := context.Background()

	if _, err := repo.CreateUserKey(ctx, uid, "alice-laptop", "ukunaffe", "uksha"); err != nil {
		t.Fatalf("CreateUserKey: %v", err)
	}

	// Soft-delete an unrelated project (alice may or may not be a member —
	// her user-owned key has no FK to it; lifecycle should be independent).
	rawSoftDeleteProject(t, db, pid)

	got, err := repo.FindByPrefixSha(ctx, "ukunaffe", "uksha")
	if err != nil {
		t.Fatalf("user-owned key resolve: %v", err)
	}
	if got.OwnerKind != "user" || got.OwnerUserID == nil || *got.OwnerUserID != uid {
		t.Fatalf("unexpected owner shape: %+v", got)
	}
}

// TestAPIKeysRepo_FindByPrefixSha_LiveProjectStillWorks pins regression
// check — live project + project-owned key still resolves.
func TestAPIKeysRepo_FindByPrefixSha_LiveProjectStillWorks(t *testing.T) {
	db := sqlitetest.New(t)
	pid := seedProject(t, db, "live-proj")
	repo := metadata.NewAPIKeysRepo(db)
	ctx := context.Background()

	id, err := repo.CreateProjectKey(ctx, pid, "ci-live", "lvpref01", "lvsha")
	if err != nil {
		t.Fatalf("CreateProjectKey: %v", err)
	}
	got, err := repo.FindByPrefixSha(ctx, "lvpref01", "lvsha")
	if err != nil {
		t.Fatalf("FindByPrefixSha: %v", err)
	}
	if got.ID != id || got.OwnerKind != "project" || got.OwnerProjectID == nil || *got.OwnerProjectID != pid {
		t.Fatalf("unexpected: %+v", got)
	}
}

// TestAPIKeysRepo_FindByPrefixSha_RevokedReturnsNotFound pins existing
// behavior — revoked rows still collapse to ErrNotFound (the new JOIN
// must not break this).
func TestAPIKeysRepo_FindByPrefixSha_RevokedReturnsNotFound(t *testing.T) {
	db := sqlitetest.New(t)
	pid := seedProject(t, db, "revrec-proj")
	repo := metadata.NewAPIKeysRepo(db)
	ctx := context.Background()

	id, err := repo.CreateProjectKey(ctx, pid, "ci-rev", "rvpref01", "rvsha")
	if err != nil {
		t.Fatalf("CreateProjectKey: %v", err)
	}
	if err := repo.Revoke(ctx, id); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	_, err = repo.FindByPrefixSha(ctx, "rvpref01", "rvsha")
	if !errors.Is(err, metadata.ErrNotFound) {
		t.Fatalf("post-revoke: want ErrNotFound, got %v", err)
	}
}

// TestAPIKeysRepo_FindByID_DeletedProject mirrors the FindByPrefixSha
// deleted-project test for FindByID — used by /v2 Bearer middleware to
// re-resolve actor on every request.
func TestAPIKeysRepo_FindByID_DeletedProject(t *testing.T) {
	db := sqlitetest.New(t)
	pid := seedProject(t, db, "fbid-deleted")
	repo := metadata.NewAPIKeysRepo(db)
	ctx := context.Background()

	id, err := repo.CreateProjectKey(ctx, pid, "ci", "fbidpref", "fbidsha")
	if err != nil {
		t.Fatalf("CreateProjectKey: %v", err)
	}
	// Sanity.
	if _, err := repo.FindByID(ctx, id); err != nil {
		t.Fatalf("pre-soft-delete: want resolve, got %v", err)
	}

	rawSoftDeleteProject(t, db, pid)

	_, err = repo.FindByID(ctx, id)
	if !errors.Is(err, metadata.ErrNotFound) {
		t.Fatalf("post-soft-delete: want ErrNotFound, got %v", err)
	}
}

// TestAPIKeysRepo_FindByID_UserOwnedUnaffected mirrors the UserOwnedUnaffected
// test for FindByID — user-owned key resolution unaffected by project lifecycle.
func TestAPIKeysRepo_FindByID_UserOwnedUnaffected(t *testing.T) {
	db := sqlitetest.New(t)
	uid := seedUser(t, db, "alice")
	pid := seedProject(t, db, "fbid-unrelated-proj")
	repo := metadata.NewAPIKeysRepo(db)
	ctx := context.Background()

	id, err := repo.CreateUserKey(ctx, uid, "alice-laptop", "fbidup01", "fbidupsh")
	if err != nil {
		t.Fatalf("CreateUserKey: %v", err)
	}

	rawSoftDeleteProject(t, db, pid)

	got, err := repo.FindByID(ctx, id)
	if err != nil {
		t.Fatalf("FindByID user-owned: %v", err)
	}
	if got.OwnerKind != "user" || got.OwnerUserID == nil || *got.OwnerUserID != uid {
		t.Fatalf("unexpected: %+v", got)
	}
}

func TestAPIKeysRepo_RevokeExcludesFromFind(t *testing.T) {
	db := sqlitetest.New(t)
	uid := seedUser(t, db, "alice")
	repo := metadata.NewAPIKeysRepo(db)
	ctx := context.Background()
	id, err := repo.CreateUserKey(ctx, uid, "k", "abcdefgh", "sha")
	if err != nil {
		t.Fatalf("CreateUserKey: %v", err)
	}
	if err := repo.Revoke(ctx, id); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	_, err = repo.FindByPrefixSha(ctx, "abcdefgh", "sha")
	if !errors.Is(err, metadata.ErrNotFound) {
		t.Fatalf("post-revoke: %v, want ErrNotFound", err)
	}
}
