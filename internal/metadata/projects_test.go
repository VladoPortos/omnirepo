package metadata_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/metadata/sqlitetest"
)

func TestProjectsRepo_CreateAndFind(t *testing.T) {
	db := sqlitetest.New(t)
	r := metadata.NewProjectsRepo(db)
	ctx := context.Background()

	id, err := r.Create(ctx, "dxc", "desc")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if id <= 0 {
		t.Fatalf("expected id>0, got %d", id)
	}
	got, err := r.FindByName(ctx, "dxc")
	if err != nil {
		t.Fatalf("find by name: %v", err)
	}
	if got.Name != "dxc" || got.DescriptionMD != "desc" {
		t.Fatalf("unexpected: %+v", got)
	}
}

// TestProjectsRepo_CreateInTxRollsBackWithMembers pins audit finding #7:
// when CreateInTx + MembersRepo.AddInTx are composed inside a single writer
// tx, a failure in the second step must leave the DB with NO project row
// (no orphan). Pre-fix, Projects.Create and Members.Add were independent
// transactions and a failure in the second left an orphan project.
func TestProjectsRepo_CreateInTxRollsBackWithMembers(t *testing.T) {
	db := sqlitetest.New(t)
	projects := metadata.NewProjectsRepo(db)
	members := metadata.NewMembersRepo(db)
	ctx := context.Background()

	// Compose project insert + membership insert in a single writer tx
	// where the membership step deliberately fails (userID=0 violates the
	// FK to users.id). The whole tx must roll back.
	err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		id, insErr := projects.CreateInTx(ctx, tx, "doomed", "")
		if insErr != nil {
			return insErr
		}
		// userID=9999 does not exist — FK violation aborts the tx.
		return members.AddInTx(ctx, tx, id, 9999, "maintainer")
	})
	if err == nil {
		t.Fatal("expected tx error from FK failure")
	}

	// Project must NOT exist post-rollback.
	if _, ferr := projects.FindByName(ctx, "doomed"); !errors.Is(ferr, metadata.ErrNotFound) {
		t.Fatalf("expected ErrNotFound after rollback, got %v", ferr)
	}
}

func TestProjectsRepo_UniqueName(t *testing.T) {
	db := sqlitetest.New(t)
	r := metadata.NewProjectsRepo(db)
	ctx := context.Background()
	if _, err := r.Create(ctx, "p1", ""); err != nil {
		t.Fatal(err)
	}
	_, err := r.Create(ctx, "p1", "")
	if err == nil {
		t.Fatalf("expected duplicate error")
	}
	if !strings.Contains(strings.ToUpper(err.Error()), "UNIQUE") && !strings.Contains(err.Error(), "constraint") {
		t.Fatalf("unexpected err: %v", err)
	}
}

func TestProjectsRepo_SoftDelete(t *testing.T) {
	db := sqlitetest.New(t)
	r := metadata.NewProjectsRepo(db)
	ctx := context.Background()
	id, err := r.Create(ctx, "p1", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.SoftDelete(ctx, id); err != nil {
		t.Fatal(err)
	}
	_, err = r.FindByName(ctx, "p1")
	if !errors.Is(err, metadata.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// TestProjectsRepo_SoftDeleteCascade pins LIFECYCLE-01..03 cascade behavior
// at the ProjectsRepo level. Soft-deleting a project must also revoke every
// live S3 access key, soft-delete every live S3 bucket, and revoke every live
// project-owned api_key for that project — all in one tx. User-owned api_keys
// for that project's members are EXPLICITLY untouched (LIFECYCLE-03 owner_kind
// filter).
func TestProjectsRepo_SoftDeleteCascade(t *testing.T) {
	db := sqlitetest.New(t)
	ctx := context.Background()
	r := metadata.NewProjectsRepo(db)
	pid, err := r.Create(ctx, "casc", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	uid := seedUser(t, db, "casc-user")
	akeys := metadata.NewAPIKeysRepo(db)
	skeys := metadata.NewS3KeysRepo(db)

	// Two live S3 keys.
	for _, akid := range []string{"AKIA-PSC-1", "AKIA-PSC-2"} {
		if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
			_, err := skeys.Insert(ctx, tx, &metadata.S3AccessKey{
				ProjectID: pid, AccessKeyID: akid, SecretEnc: []byte("x"),
				Label: akid, CreatedByUserID: uid,
			})
			return err
		}); err != nil {
			t.Fatalf("insert s3 key: %v", err)
		}
	}
	// Two live buckets.
	if _, err := db.Writer.ExecContext(ctx,
		`INSERT INTO s3_buckets(name, project_id) VALUES ('psc-1', ?), ('psc-2', ?)`, pid, pid,
	); err != nil {
		t.Fatalf("insert buckets: %v", err)
	}
	// One project-owned api_key + one user-owned api_key.
	pkID, err := akeys.CreateProjectKey(ctx, pid, "ci", "pscpref0", "shaPSC")
	if err != nil {
		t.Fatalf("CreateProjectKey: %v", err)
	}
	ukID, err := akeys.CreateUserKey(ctx, uid, "alice", "uscpref0", "shaUSC")
	if err != nil {
		t.Fatalf("CreateUserKey: %v", err)
	}

	// SoftDelete fires the cascade.
	if err := r.SoftDelete(ctx, pid); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}

	// Project row tombstoned.
	var pdel sql.NullString
	_ = db.Reader.QueryRowContext(ctx, `SELECT deleted_at FROM projects WHERE id=?`, pid).Scan(&pdel)
	if !pdel.Valid {
		t.Fatalf("project deleted_at NULL, want stamp")
	}

	// All live S3 keys revoked.
	for _, akid := range []string{"AKIA-PSC-1", "AKIA-PSC-2"} {
		var rev sql.NullString
		_ = db.Reader.QueryRowContext(ctx,
			`SELECT revoked_at FROM s3_access_keys WHERE access_key_id=?`, akid,
		).Scan(&rev)
		if !rev.Valid {
			t.Fatalf("s3 key %s revoked_at NULL post-cascade", akid)
		}
	}
	// Buckets soft-deleted.
	for _, bn := range []string{"psc-1", "psc-2"} {
		var bdel sql.NullString
		_ = db.Reader.QueryRowContext(ctx,
			`SELECT deleted_at FROM s3_buckets WHERE name=?`, bn,
		).Scan(&bdel)
		if !bdel.Valid {
			t.Fatalf("bucket %s deleted_at NULL post-cascade", bn)
		}
	}
	// Project-owned api key revoked.
	var pkRev sql.NullString
	_ = db.Reader.QueryRowContext(ctx,
		`SELECT revoked_at FROM api_keys WHERE id=?`, pkID,
	).Scan(&pkRev)
	if !pkRev.Valid {
		t.Fatalf("project api key revoked_at NULL post-cascade")
	}
	// User-owned api key UNTOUCHED (owner_kind filter must spare it).
	var ukRev sql.NullString
	_ = db.Reader.QueryRowContext(ctx,
		`SELECT revoked_at FROM api_keys WHERE id=?`, ukID,
	).Scan(&ukRev)
	if ukRev.Valid {
		t.Fatalf("user-owned api key revoked_at=%q want NULL (owner_kind filter)", ukRev.String)
	}

	// Cross-table timestamp equality — every cascaded row stores a value byte-
	// identical to projects.deleted_at (prove via WHERE-equality probe).
	cascadeTS := pdel.String
	var match int
	_ = db.Reader.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM s3_access_keys WHERE project_id=? AND revoked_at = ?`, pid, cascadeTS,
	).Scan(&match)
	if match != 2 {
		t.Fatalf("s3 keys cascade-TS equality probe: %d want 2", match)
	}
	_ = db.Reader.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM s3_buckets WHERE project_id=? AND deleted_at = ?`, pid, cascadeTS,
	).Scan(&match)
	if match != 2 {
		t.Fatalf("buckets cascade-TS equality probe: %d want 2", match)
	}
	_ = db.Reader.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM api_keys WHERE owner_project_id=? AND revoked_at = ?`, pid, cascadeTS,
	).Scan(&match)
	if match != 1 {
		t.Fatalf("project api keys cascade-TS equality probe: %d want 1", match)
	}
}

// TestProjectsRepo_SoftDeleteCascade_Idempotent — pre-revoked / pre-deleted
// child rows retain their original timestamp, NOT the cascade timestamp.
func TestProjectsRepo_SoftDeleteCascade_Idempotent(t *testing.T) {
	db := sqlitetest.New(t)
	ctx := context.Background()
	r := metadata.NewProjectsRepo(db)
	pid, err := r.Create(ctx, "casc-idem", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	uid := seedUser(t, db, "casc-idem-user")
	skeys := metadata.NewS3KeysRepo(db)

	var liveID, preID int64
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		v, err := skeys.Insert(ctx, tx, &metadata.S3AccessKey{
			ProjectID: pid, AccessKeyID: "AKIA-IDEM-LIVE", SecretEnc: []byte("x"),
			Label: "live", CreatedByUserID: uid,
		})
		liveID = v
		if err != nil {
			return err
		}
		v, err = skeys.Insert(ctx, tx, &metadata.S3AccessKey{
			ProjectID: pid, AccessKeyID: "AKIA-IDEM-PRE", SecretEnc: []byte("x"),
			Label: "pre", CreatedByUserID: uid,
		})
		preID = v
		return err
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	const preTS = "1999-01-01 00:00:00"
	if _, err := db.Writer.ExecContext(ctx,
		`UPDATE s3_access_keys SET revoked_at=? WHERE id=?`, preTS, preID,
	); err != nil {
		t.Fatalf("pre-revoke: %v", err)
	}

	if err := r.SoftDelete(ctx, pid); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}

	// Pre-revoked key keeps preTS (independent revoke must survive).
	var match int
	_ = db.Reader.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM s3_access_keys WHERE id=? AND revoked_at = ?`, preID, preTS,
	).Scan(&match)
	if match != 1 {
		t.Fatalf("pre-revoked key clobbered (equality probe got %d)", match)
	}
	// Live key revoked at the cascade TS.
	var rev sql.NullString
	_ = db.Reader.QueryRowContext(ctx,
		`SELECT revoked_at FROM s3_access_keys WHERE id=?`, liveID,
	).Scan(&rev)
	if !rev.Valid {
		t.Fatal("live key revoked_at NULL, want stamp")
	}
}

// TestProjectsRepo_RestoreCascadeSymmetry — Restore reverses the cascade for
// rows whose timestamp matches the project's prior deleted_at, leaves others
// alone (D-05).
func TestProjectsRepo_RestoreCascadeSymmetry(t *testing.T) {
	db := sqlitetest.New(t)
	ctx := context.Background()
	r := metadata.NewProjectsRepo(db)
	pid, err := r.Create(ctx, "casc-rest", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	uid := seedUser(t, db, "casc-rest-user")
	skeys := metadata.NewS3KeysRepo(db)

	var liveID, preID int64
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		v, err := skeys.Insert(ctx, tx, &metadata.S3AccessKey{
			ProjectID: pid, AccessKeyID: "AKIA-REST-LIVE", SecretEnc: []byte("x"),
			Label: "live", CreatedByUserID: uid,
		})
		liveID = v
		if err != nil {
			return err
		}
		v, err = skeys.Insert(ctx, tx, &metadata.S3AccessKey{
			ProjectID: pid, AccessKeyID: "AKIA-REST-PRE", SecretEnc: []byte("x"),
			Label: "pre", CreatedByUserID: uid,
		})
		preID = v
		return err
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if _, err := db.Writer.ExecContext(ctx,
		`INSERT INTO s3_buckets(name, project_id) VALUES ('rest-bkt', ?)`, pid,
	); err != nil {
		t.Fatalf("insert bucket: %v", err)
	}
	const preTS = "1999-01-01 00:00:00"
	if _, err := db.Writer.ExecContext(ctx,
		`UPDATE s3_access_keys SET revoked_at=? WHERE id=?`, preTS, preID,
	); err != nil {
		t.Fatalf("pre-revoke: %v", err)
	}

	if err := r.SoftDelete(ctx, pid); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}
	if err := r.Restore(ctx, pid); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	// Live key restored.
	var rev sql.NullString
	_ = db.Reader.QueryRowContext(ctx,
		`SELECT revoked_at FROM s3_access_keys WHERE id=?`, liveID,
	).Scan(&rev)
	if rev.Valid {
		t.Fatalf("live key revoked_at=%q want NULL post-restore", rev.String)
	}
	// Pre-revoked key still revoked at preTS.
	var match int
	_ = db.Reader.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM s3_access_keys WHERE id=? AND revoked_at = ?`, preID, preTS,
	).Scan(&match)
	if match != 1 {
		t.Fatalf("pre-revoked key spuriously restored (equality probe got %d)", match)
	}
	// Bucket restored.
	var bdel sql.NullString
	_ = db.Reader.QueryRowContext(ctx,
		`SELECT deleted_at FROM s3_buckets WHERE name='rest-bkt'`,
	).Scan(&bdel)
	if bdel.Valid {
		t.Fatalf("bucket deleted_at=%q want NULL post-restore", bdel.String)
	}
	// Project itself restored.
	var pdel sql.NullString
	_ = db.Reader.QueryRowContext(ctx, `SELECT deleted_at FROM projects WHERE id=?`, pid).Scan(&pdel)
	if pdel.Valid {
		t.Fatalf("project deleted_at=%q want NULL post-restore", pdel.String)
	}
}

// TestProjectsRepo_RestoreIfNameFree_Cascade — RestoreIfNameFree must run the
// same reverse-cascade as Restore.
func TestProjectsRepo_RestoreIfNameFree_Cascade(t *testing.T) {
	db := sqlitetest.New(t)
	ctx := context.Background()
	r := metadata.NewProjectsRepo(db)
	pid, err := r.Create(ctx, "casc-name", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	uid := seedUser(t, db, "casc-name-user")
	akeys := metadata.NewAPIKeysRepo(db)
	pkID, err := akeys.CreateProjectKey(ctx, pid, "ci", "rnpref01", "shaRN")
	if err != nil {
		t.Fatalf("CreateProjectKey: %v", err)
	}
	_ = uid

	if err := r.SoftDelete(ctx, pid); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}
	// Verify revoked.
	var rev sql.NullString
	_ = db.Reader.QueryRowContext(ctx, `SELECT revoked_at FROM api_keys WHERE id=?`, pkID).Scan(&rev)
	if !rev.Valid {
		t.Fatal("project api key not revoked post-SoftDelete")
	}

	if err := r.RestoreIfNameFree(ctx, pid); err != nil {
		t.Fatalf("RestoreIfNameFree: %v", err)
	}
	_ = db.Reader.QueryRowContext(ctx, `SELECT revoked_at FROM api_keys WHERE id=?`, pkID).Scan(&rev)
	if rev.Valid {
		t.Fatalf("project api key revoked_at=%q want NULL after RestoreIfNameFree", rev.String)
	}
}

// TestProjectsRepo_RestoreIfNameFree_NameCollisionPreservesCascade — when
// RestoreIfNameFree refuses (name taken), the cascade must NOT have fired
// (project + child rows stay tombstoned).
func TestProjectsRepo_RestoreIfNameFree_NameCollisionPreservesCascade(t *testing.T) {
	db := sqlitetest.New(t)
	ctx := context.Background()
	r := metadata.NewProjectsRepo(db)
	pid, err := r.Create(ctx, "collide", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	akeys := metadata.NewAPIKeysRepo(db)
	pkID, err := akeys.CreateProjectKey(ctx, pid, "ci", "ccolpref", "shaCOL")
	if err != nil {
		t.Fatalf("CreateProjectKey: %v", err)
	}
	if err := r.SoftDelete(ctx, pid); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}
	// Claim the name with a new live project.
	if _, err := r.Create(ctx, "collide", ""); err != nil {
		t.Fatalf("create collision: %v", err)
	}
	// RestoreIfNameFree must refuse.
	if err := r.RestoreIfNameFree(ctx, pid); !errors.Is(err, metadata.ErrNameTaken) {
		t.Fatalf("RestoreIfNameFree: %v, want ErrNameTaken", err)
	}
	// Cascade state preserved — project still soft-deleted, key still revoked.
	var pdel, rev sql.NullString
	_ = db.Reader.QueryRowContext(ctx, `SELECT deleted_at FROM projects WHERE id=?`, pid).Scan(&pdel)
	if !pdel.Valid {
		t.Fatal("project deleted_at NULL after refused RestoreIfNameFree")
	}
	_ = db.Reader.QueryRowContext(ctx, `SELECT revoked_at FROM api_keys WHERE id=?`, pkID).Scan(&rev)
	if !rev.Valid {
		t.Fatal("project api key revoked_at NULL after refused RestoreIfNameFree")
	}
}

// -----------------------------------------------------------------------------
// Phase 01 Plan 01-03 (LIFECYCLE-09): Projects.SoftDelete cascades FTS prune
// to every live repo; Projects.Restore reverse-cascades FTS reindex only for
// repos cascaded by THIS soft-delete (timestamp-equality filter).
// -----------------------------------------------------------------------------

// TestProjects_SoftDelete_CascadesPruneFTS — project SoftDelete must:
//   - cascade-soft-delete every live repo using cascadeTS (NOT CURRENT_TIMESTAMP)
//   - prune FTS rows for each cascaded repo
//   - leave already-soft-deleted repos at their prior tombstone (different TS)
func TestProjects_SoftDelete_CascadesPruneFTS(t *testing.T) {
	db := sqlitetest.New(t)
	ctx := context.Background()
	r := metadata.NewProjectsRepo(db)
	repos := metadata.NewReposRepo(db)

	pid, err := r.Create(ctx, "casc-fts", "")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	r1id, err := repos.Create(ctx, pid, "rpm", "r1", "", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	r2id, err := repos.Create(ctx, pid, "rpm", "r2", "", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	r3id, err := repos.Create(ctx, pid, "rpm", "r3", "", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Seed 1 rpm_fts row per repo.
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		for _, rid := range []int64{r1id, r2id, r3id} {
			if err := metadata.IndexRPM(ctx, tx, rid, "pkg", "1.0", "x86_64", ""); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed FTS: %v", err)
	}

	// Independently soft-delete r3 BEFORE the project cascade — its
	// deleted_at must NOT be clobbered by the project cascade and Restore
	// must NOT bring it back (different TS). r3's FTS is also pruned by
	// Repos.SoftDelete in the same step.
	//
	// We force r3's tombstone to a fixed legacy string so the cascade-vs-
	// independent equality probe doesn't depend on modernc/sqlite's
	// TIMESTAMP-affinity normalization (plan 01-01 discovery — read-back
	// returns ISO-8601 even when the WHERE-equality compare needs the raw
	// stored bytes).
	if err := repos.SoftDelete(ctx, r3id); err != nil {
		t.Fatalf("SoftDelete r3: %v", err)
	}
	const r3PriorTS = "1999-01-01 00:00:00"
	if _, err := db.Writer.ExecContext(ctx,
		`UPDATE repos SET deleted_at=? WHERE id=?`, r3PriorTS, r3id,
	); err != nil {
		t.Fatalf("override r3 TS: %v", err)
	}

	// Cascade-soft-delete via the project.
	if err := r.SoftDelete(ctx, pid); err != nil {
		t.Fatalf("Project SoftDelete: %v", err)
	}

	// All three repos have FTS pruned (r1, r2 via project cascade; r3 was already pruned).
	for _, rid := range []int64{r1id, r2id, r3id} {
		var n int
		_ = db.Reader.QueryRowContext(ctx, `SELECT COUNT(*) FROM rpm_fts WHERE repo_id=?`, rid).Scan(&n)
		if n != 0 {
			t.Errorf("rpm_fts for repo %d=%d want 0", rid, n)
		}
	}

	// r1, r2 deleted_at == project.deleted_at (cascade marker).
	var pdel sql.NullString
	_ = db.Reader.QueryRowContext(ctx, `SELECT deleted_at FROM projects WHERE id=?`, pid).Scan(&pdel)
	if !pdel.Valid {
		t.Fatal("project deleted_at NULL")
	}
	for _, rid := range []int64{r1id, r2id} {
		var n int
		_ = db.Reader.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM repos WHERE id=? AND deleted_at = ?`, rid, pdel.String,
		).Scan(&n)
		if n != 1 {
			t.Errorf("repo %d cascade-TS equality probe got %d want 1", rid, n)
		}
	}
	// r3 still has its original TS, NOT the project's cascade TS.
	var n int
	_ = db.Reader.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM repos WHERE id=? AND deleted_at = ?`, r3id, r3PriorTS,
	).Scan(&n)
	if n != 1 {
		t.Errorf("r3 prior-TS equality probe got %d want 1 (independent soft-delete must survive)", n)
	}
}

// TestProjects_Restore_CascadesReindexFTS_TimestampEqualityOnly — after
// project Restore, only repos cascaded by THIS soft-delete (deleted_at ==
// priorTS) get reindexed; independently soft-deleted repos stay tombstoned.
func TestProjects_Restore_CascadesReindexFTS_TimestampEqualityOnly(t *testing.T) {
	db := sqlitetest.New(t)
	ctx := context.Background()
	rpmRepo := metadata.NewRPMPackagesRepo(db)
	rx := metadata.NewFTSReindexer(db, rpmRepo, metadata.NewDEBPackagesRepo(db),
		metadata.NewPyPIFilesRepo(db), metadata.NewHelmChartsRepo(db))
	r := metadata.NewProjectsRepo(db).WithReindexer(rx)
	repos := metadata.NewReposRepo(db).WithReindexer(rx)

	pid, err := r.Create(ctx, "casc-rest", "")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	r1id, _ := repos.Create(ctx, pid, "rpm", "r1", "", nil, nil, nil)
	r2id, _ := repos.Create(ctx, pid, "rpm", "r2", "", nil, nil, nil)
	r3id, _ := repos.Create(ctx, pid, "rpm", "r3", "", nil, nil, nil)

	// Seed 1 rpm_packages row per repo (base table, untouched by SoftDelete).
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		for i, rid := range []int64{r1id, r2id, r3id} {
			if _, err := rpmRepo.Insert(ctx, tx, &metadata.RPMPackage{
				RepoID: rid, Name: "pkg", Epoch: 0, Version: "1.0", Release: "1",
				Arch: "x86_64", Summary: "s",
				Digest: "sha256:r" + string(rune('0'+i)), Filename: "pkg.rpm",
			}); err != nil {
				return err
			}
			if err := metadata.IndexRPM(ctx, tx, rid, "pkg", "1.0", "x86_64", ""); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Independently soft-delete r3.
	if err := repos.SoftDelete(ctx, r3id); err != nil {
		t.Fatalf("SoftDelete r3: %v", err)
	}

	// Now project SoftDelete (cascades r1, r2; r3 already deleted at different TS).
	if err := r.SoftDelete(ctx, pid); err != nil {
		t.Fatalf("Project SoftDelete: %v", err)
	}
	// Restore.
	if err := r.Restore(ctx, pid); err != nil {
		t.Fatalf("Project Restore: %v", err)
	}

	// r1, r2 reindexed (1 rpm_fts each, re-derived from base table).
	for _, rid := range []int64{r1id, r2id} {
		var n int
		_ = db.Reader.QueryRowContext(ctx, `SELECT COUNT(*) FROM rpm_fts WHERE repo_id=?`, rid).Scan(&n)
		if n != 1 {
			t.Errorf("rpm_fts for restored repo %d=%d want 1 (reindex)", rid, n)
		}
		var del sql.NullString
		_ = db.Reader.QueryRowContext(ctx, `SELECT deleted_at FROM repos WHERE id=?`, rid).Scan(&del)
		if del.Valid {
			t.Errorf("repo %d deleted_at=%q want NULL post-restore", rid, del.String)
		}
	}
	// r3 still soft-deleted; FTS still empty (independent soft-delete must survive).
	var del sql.NullString
	_ = db.Reader.QueryRowContext(ctx, `SELECT deleted_at FROM repos WHERE id=?`, r3id).Scan(&del)
	if !del.Valid {
		t.Fatal("r3 spuriously restored by project cascade")
	}
	var n int
	_ = db.Reader.QueryRowContext(ctx, `SELECT COUNT(*) FROM rpm_fts WHERE repo_id=?`, r3id).Scan(&n)
	if n != 0 {
		t.Errorf("rpm_fts for r3=%d want 0 (still soft-deleted)", n)
	}
}

func TestProjectsRepo_ListAll(t *testing.T) {
	db := sqlitetest.New(t)
	r := metadata.NewProjectsRepo(db)
	ctx := context.Background()
	for _, n := range []string{"b", "a", "c"} {
		if _, err := r.Create(ctx, n, ""); err != nil {
			t.Fatal(err)
		}
	}
	list, err := r.ListAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 || list[0].Name != "a" || list[2].Name != "c" {
		t.Fatalf("unexpected order: %+v", list)
	}
}
