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

func TestReposRepo_CreateAndFind(t *testing.T) {
	db := sqlitetest.New(t)
	ctx := context.Background()
	pid := seedProject(t, db, "p")

	r := metadata.NewReposRepo(db)
	id, err := r.Create(ctx, pid, "docker", "r1", "", nil, nil, nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := r.FindByTriple(ctx, pid, "docker", "r1")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != id || got.AutoScan != true || got.BlockOnSeverity != "none" {
		t.Fatalf("defaults mismatch: %+v", got)
	}
}

func TestReposRepo_RejectsInvalidType(t *testing.T) {
	db := sqlitetest.New(t)
	ctx := context.Background()
	pid := seedProject(t, db, "p")
	r := metadata.NewReposRepo(db)
	_, err := r.Create(ctx, pid, "bogus", "r", "", nil, nil, nil)
	if err == nil {
		t.Fatalf("expected CHECK-constraint error")
	}
	if !strings.Contains(err.Error(), "CHECK") && !strings.Contains(err.Error(), "constraint") {
		t.Fatalf("unexpected: %v", err)
	}
}

func TestReposRepo_UniqueWithinProjectType(t *testing.T) {
	db := sqlitetest.New(t)
	ctx := context.Background()
	pid := seedProject(t, db, "p")
	r := metadata.NewReposRepo(db)
	if _, err := r.Create(ctx, pid, "docker", "r", "", nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Create(ctx, pid, "docker", "r", "", nil, nil, nil); err == nil {
		t.Fatalf("expected unique error")
	}
	// Same name in a different type is fine.
	if _, err := r.Create(ctx, pid, "rpm", "r", "", nil, nil, nil); err != nil {
		t.Fatalf("same-name different-type failed: %v", err)
	}
}

func TestReposRepo_SoftDeleteListExcludes(t *testing.T) {
	db := sqlitetest.New(t)
	ctx := context.Background()
	pid := seedProject(t, db, "p")
	r := metadata.NewReposRepo(db)
	id, err := r.Create(ctx, pid, "docker", "r1", "", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.SoftDelete(ctx, id); err != nil {
		t.Fatal(err)
	}
	list, err := r.ListByProject(ctx, pid)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("expected empty, got %+v", list)
	}
	_, err = r.FindByTriple(ctx, pid, "docker", "r1")
	if !errors.Is(err, metadata.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// -----------------------------------------------------------------------------
// Phase 01 Plan 01-03 (LIFECYCLE-09): Repos.SoftDelete prunes FTS;
// Repos.Restore reindexes from base tables; Repos.SoftDelete is idempotent.
// -----------------------------------------------------------------------------

// TestRepos_SoftDelete_PrunesFTS — soft-deleting a repo removes every per-protocol
// FTS row keyed to that repo, in the same WriteTx as the row UPDATE.
func TestRepos_SoftDelete_PrunesFTS(t *testing.T) {
	db := sqlitetest.New(t)
	ctx := context.Background()
	pid := seedProject(t, db, "p-prune")
	r := metadata.NewReposRepo(db)
	id, err := r.Create(ctx, pid, "rpm", "r-prune", "", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Seed 2 rpm_fts + 1 artifacts_fts + 1 repos_fts.
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		if err := metadata.IndexRepo(ctx, tx, id, "r-prune", "p-prune", "", "rpm"); err != nil {
			return err
		}
		if err := metadata.IndexRPM(ctx, tx, id, "httpd", "2.4.62", "x86_64", "Apache"); err != nil {
			return err
		}
		if err := metadata.IndexRPM(ctx, tx, id, "nginx", "1.25", "x86_64", "web"); err != nil {
			return err
		}
		return metadata.IndexArtifact(ctx, tx, id, "art", "1.0", "sha256:art")
	}); err != nil {
		t.Fatalf("seed FTS: %v", err)
	}

	if err := r.SoftDelete(ctx, id); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}

	for _, q := range []struct {
		name, sql string
	}{
		{"repos_fts", `SELECT COUNT(*) FROM repos_fts WHERE rowid=?`},
		{"rpm_fts", `SELECT COUNT(*) FROM rpm_fts WHERE repo_id=?`},
		{"artifacts_fts", `SELECT COUNT(*) FROM artifacts_fts WHERE repo_id=?`},
	} {
		var n int
		_ = db.Reader.QueryRowContext(ctx, q.sql, id).Scan(&n)
		if n != 0 {
			t.Errorf("%s after SoftDelete=%d want 0", q.name, n)
		}
	}
}

// TestRepos_Restore_ReindexesFTS — after SoftDelete, base tables are
// untouched. Inserting a fresh rpm_packages row directly while the repo is
// soft-deleted then calling Restore must re-derive rpm_fts from base tables
// (loss-free reindex).
func TestRepos_Restore_ReindexesFTS(t *testing.T) {
	db := sqlitetest.New(t)
	ctx := context.Background()
	pid := seedProject(t, db, "p-restore")
	rpmRepo := metadata.NewRPMPackagesRepo(db)
	rx := metadata.NewFTSReindexer(db, rpmRepo, metadata.NewDEBPackagesRepo(db),
		metadata.NewPyPIFilesRepo(db), metadata.NewHelmChartsRepo(db))
	r := metadata.NewReposRepo(db).WithReindexer(rx)

	id, err := r.Create(ctx, pid, "rpm", "r-restore", "", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Insert one rpm_packages row pre-SoftDelete (FTS not seeded — Reindex builds it).
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := rpmRepo.Insert(ctx, tx, &metadata.RPMPackage{
			RepoID: id, Name: "httpd", Epoch: 0, Version: "2.4.62", Release: "1",
			Arch: "x86_64", Summary: "Apache", Digest: "sha256:rpm1", Filename: "httpd.rpm",
		})
		return err
	}); err != nil {
		t.Fatalf("insert rpm: %v", err)
	}

	if err := r.SoftDelete(ctx, id); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}

	// Insert a NEW rpm row directly while the repo is soft-deleted — bypasses
	// FTS path. Restore must re-derive FTS from this base row + the original.
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := rpmRepo.Insert(ctx, tx, &metadata.RPMPackage{
			RepoID: id, Name: "nginx", Epoch: 0, Version: "1.25", Release: "1",
			Arch: "x86_64", Summary: "web", Digest: "sha256:rpm2", Filename: "nginx.rpm",
		})
		return err
	}); err != nil {
		t.Fatalf("insert rpm 2: %v", err)
	}

	if err := r.Restore(ctx, id); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	// rpm_fts should now have 2 rows for repoID (re-derived from rpm_packages).
	var n int
	_ = db.Reader.QueryRowContext(ctx, `SELECT COUNT(*) FROM rpm_fts WHERE repo_id=?`, id).Scan(&n)
	if n != 2 {
		t.Errorf("rpm_fts after Restore=%d want 2 (loss-free reindex)", n)
	}
}

// TestRepos_SoftDelete_Idempotent — calling SoftDelete twice doesn't error
// and doesn't double-delete. Pruning cves_fts via the conditional NOT_IN
// chain is also a no-op on the second call.
func TestRepos_SoftDelete_Idempotent(t *testing.T) {
	db := sqlitetest.New(t)
	ctx := context.Background()
	pid := seedProject(t, db, "p-idem")
	r := metadata.NewReposRepo(db)
	id, err := r.Create(ctx, pid, "rpm", "r-idem", "", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		return metadata.IndexRPM(ctx, tx, id, "x", "1.0", "x86_64", "")
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := r.SoftDelete(ctx, id); err != nil {
		t.Fatalf("SoftDelete pass 1: %v", err)
	}
	if err := r.SoftDelete(ctx, id); err != nil {
		t.Fatalf("SoftDelete pass 2 (must be no-op): %v", err)
	}
	var n int
	_ = db.Reader.QueryRowContext(ctx, `SELECT COUNT(*) FROM rpm_fts WHERE repo_id=?`, id).Scan(&n)
	if n != 0 {
		t.Errorf("rpm_fts after 2 SoftDeletes=%d want 0", n)
	}
}

// --------------------------------------------------------------------------
// Phase 02-11: Update + WipeDocker + WipeRaw helpers (REPO-05, REPO-07).
// --------------------------------------------------------------------------

func TestReposRepo_Update_PartialOnlyAutoScan(t *testing.T) {
	db := sqlitetest.New(t)
	ctx := context.Background()
	pid := seedProject(t, db, "p")
	r := metadata.NewReposRepo(db)
	id, err := r.Create(ctx, pid, "docker", "r1", "initial desc", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	before, err := r.FindByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !before.AutoScan {
		t.Fatalf("expected default auto_scan=true")
	}
	// Only flip auto_scan; description_md, block_on_severity, public_read stay put.
	newScan := false
	var after metadata.Repo
	err = db.WriteTx(ctx, func(tx *sql.Tx) error {
		var uerr error
		after, uerr = r.Update(ctx, tx, id, metadata.UpdateFields{AutoScan: &newScan})
		return uerr
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if after.AutoScan != false {
		t.Fatalf("auto_scan not updated: %+v", after)
	}
	if after.DescriptionMD != "initial desc" {
		t.Fatalf("description_md must not change: %q", after.DescriptionMD)
	}
	if after.BlockOnSeverity != before.BlockOnSeverity {
		t.Fatalf("block_on_severity must not change: %q", after.BlockOnSeverity)
	}
	if after.PublicRead != before.PublicRead {
		t.Fatalf("public_read must not change: %v", after.PublicRead)
	}
}

func TestReposRepo_Update_NoFields_ReturnsCurrent(t *testing.T) {
	db := sqlitetest.New(t)
	ctx := context.Background()
	pid := seedProject(t, db, "p")
	r := metadata.NewReposRepo(db)
	id, err := r.Create(ctx, pid, "raw", "r1", "d", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var got metadata.Repo
	err = db.WriteTx(ctx, func(tx *sql.Tx) error {
		var uerr error
		got, uerr = r.Update(ctx, tx, id, metadata.UpdateFields{})
		return uerr
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != id {
		t.Fatalf("expected round-trip current repo, got id=%d", got.ID)
	}
}

func TestReposRepo_Update_AllFields(t *testing.T) {
	db := sqlitetest.New(t)
	ctx := context.Background()
	pid := seedProject(t, db, "p")
	r := metadata.NewReposRepo(db)
	id, err := r.Create(ctx, pid, "docker", "r1", "", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	desc := "new"
	scan := false
	sev := "high"
	pub := true
	var got metadata.Repo
	err = db.WriteTx(ctx, func(tx *sql.Tx) error {
		var uerr error
		got, uerr = r.Update(ctx, tx, id, metadata.UpdateFields{
			DescriptionMD:   &desc,
			AutoScan:        &scan,
			BlockOnSeverity: &sev,
			PublicRead:      &pub,
		})
		return uerr
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.DescriptionMD != desc || got.AutoScan != false || got.BlockOnSeverity != "high" || got.PublicRead != true {
		t.Fatalf("all-fields update: %+v", got)
	}
}

func TestReposRepo_Update_MissingRow(t *testing.T) {
	db := sqlitetest.New(t)
	ctx := context.Background()
	r := metadata.NewReposRepo(db)
	desc := "x"
	err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, uerr := r.Update(ctx, tx, 9999, metadata.UpdateFields{DescriptionMD: &desc})
		return uerr
	})
	if !errors.Is(err, metadata.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for missing repo, got %v", err)
	}
}

func TestReposRepo_WipeDocker_SharedBlobsSurvive(t *testing.T) {
	db := sqlitetest.New(t)
	ctx := context.Background()
	pid := seedProject(t, db, "p")
	reposRepo := metadata.NewReposRepo(db)
	r1id, err := reposRepo.Create(ctx, pid, "docker", "r1", "", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	r2id, err := reposRepo.Create(ctx, pid, "docker", "r2", "", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	blobs := metadata.NewDockerBlobsRepo(db)
	manis := metadata.NewDockerManifestsRepo(db)
	tags := metadata.NewDockerTagsRepo(db)

	// Seed blobs.
	shared := "sha256:shared"
	r1only := "sha256:r1only"
	err = db.WriteTx(ctx, func(tx *sql.Tx) error {
		if err := blobs.UpsertZeroRef(ctx, tx, shared, 100); err != nil {
			return err
		}
		if err := blobs.UpsertZeroRef(ctx, tx, r1only, 200); err != nil {
			return err
		}
		// Manifest M1 in r1 references both blobs (body embeds both digests
		// so WipeDocker's extractor can rediscover them).
		body1 := []byte(`{"schemaVersion":2,"config":{"digest":"` + shared + `"},"layers":[{"digest":"` + r1only + `"}]}`)
		if err := manis.Insert(ctx, tx, r1id, "sha256:m1", "application/vnd.oci.image.manifest.v1+json", body1); err != nil {
			return err
		}
		if err := blobs.IncRef(ctx, tx, shared); err != nil {
			return err
		}
		if err := blobs.IncRef(ctx, tx, r1only); err != nil {
			return err
		}
		// Tag in r1.
		if _, err := tags.Upsert(ctx, tx, r1id, "", "latest", "sha256:m1"); err != nil {
			return err
		}
		// Manifest M2 in r2 references shared only.
		body2 := []byte(`{"schemaVersion":2,"config":{"digest":"` + shared + `"},"layers":[]}`)
		if err := manis.Insert(ctx, tx, r2id, "sha256:m2", "application/vnd.oci.image.manifest.v1+json", body2); err != nil {
			return err
		}
		if err := blobs.IncRef(ctx, tx, shared); err != nil {
			return err
		}
		if _, err := tags.Upsert(ctx, tx, r2id, "", "latest", "sha256:m2"); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Pre-check refcounts.
	if b, _ := blobs.Stat(ctx, shared); b == nil || b.RefCount != 2 {
		t.Fatalf("shared refcount pre: %+v", b)
	}
	if b, _ := blobs.Stat(ctx, r1only); b == nil || b.RefCount != 1 {
		t.Fatalf("r1only refcount pre: %+v", b)
	}

	var count, bytesFreed int64
	err = db.WriteTx(ctx, func(tx *sql.Tx) error {
		var werr error
		count, bytesFreed, werr = reposRepo.WipeDocker(ctx, tx, r1id)
		return werr
	})
	if err != nil {
		t.Fatalf("WipeDocker: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 manifest wiped, got %d", count)
	}
	// bytes_freed should be size of r1only (unique to r1) = 200; shared survives.
	if bytesFreed != 200 {
		t.Fatalf("expected bytes_freed=200 (r1only), got %d", bytesFreed)
	}
	// Shared blob survives with refcount 1.
	b, _ := blobs.Stat(ctx, shared)
	if b == nil || b.RefCount != 1 {
		t.Fatalf("shared refcount post: %+v", b)
	}
	// r1only blob row still exists but refcount is 0 (GC will sweep it; we don't delete CAS).
	b2, _ := blobs.Stat(ctx, r1only)
	if b2 == nil || b2.RefCount != 0 {
		t.Fatalf("r1only refcount post should be 0, got %+v", b2)
	}
	// Manifests & tags in r1 gone; r2 untouched.
	if got, _ := manis.GetByDigest(ctx, r1id, "sha256:m1"); got != nil {
		t.Fatalf("manifest m1 should be gone")
	}
	if d, _ := tags.Resolve(ctx, r1id, "", "latest"); d != "" {
		t.Fatalf("tag in r1 should be gone, got %q", d)
	}
	if got, _ := manis.GetByDigest(ctx, r2id, "sha256:m2"); got == nil {
		t.Fatalf("manifest m2 in r2 must survive")
	}
	if d, _ := tags.Resolve(ctx, r2id, "", "latest"); d != "sha256:m2" {
		t.Fatalf("tag in r2 must survive, got %q", d)
	}
}

func TestReposRepo_WipeRaw(t *testing.T) {
	db := sqlitetest.New(t)
	ctx := context.Background()
	pid := seedProject(t, db, "p")
	reposRepo := metadata.NewReposRepo(db)
	rid, err := reposRepo.Create(ctx, pid, "raw", "r1", "", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw := metadata.NewRawFilesRepo(db)
	err = db.WriteTx(ctx, func(tx *sql.Tx) error {
		for i := 0; i < 50; i++ {
			if err := raw.Insert(ctx, tx, rid, fmtPath(i), int64(10+i), "text/plain", "hex"); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	var count, bytesFreed int64
	err = db.WriteTx(ctx, func(tx *sql.Tx) error {
		var werr error
		count, bytesFreed, werr = reposRepo.WipeRaw(ctx, tx, rid)
		return werr
	})
	if err != nil {
		t.Fatalf("WipeRaw: %v", err)
	}
	if count != 50 {
		t.Fatalf("expected 50 files wiped, got %d", count)
	}
	// Sum of sizes 10..59 = 50*10 + (0+1+..+49) = 500 + 1225 = 1725.
	if bytesFreed != 1725 {
		t.Fatalf("bytes_freed mismatch: got %d want 1725", bytesFreed)
	}
	files, err := raw.ListDir(ctx, rid, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Fatalf("expected 0 files after wipe, got %d", len(files))
	}
}

func fmtPath(i int) string {
	return "file-" + itoa(i) + ".txt"
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := false
	if i < 0 {
		neg = true
		i = -i
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		b[pos] = '-'
	}
	return string(b[pos:])
}

func TestReposRepo_OptionalArgsHonored(t *testing.T) {
	db := sqlitetest.New(t)
	ctx := context.Background()
	pid := seedProject(t, db, "p")
	r := metadata.NewReposRepo(db)
	autoScan := false
	bos := "high"
	pr := true
	id, err := r.Create(ctx, pid, "helm", "h1", "d", &autoScan, &bos, &pr)
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.FindByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.AutoScan != false || got.BlockOnSeverity != "high" || got.PublicRead != true {
		t.Fatalf("optional args not honored: %+v", got)
	}
}

// Phase 03 Plan 01 (D-12): metadata_state helpers.

func TestReposMetadataStateRoundTrip(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()
	pid := seedProject(t, db, "p-md")
	r := metadata.NewReposRepo(db)
	id, err := r.Create(ctx, pid, "rpm", "r-md", "", nil, nil, nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	state, lastErr, err := r.GetMetadataState(ctx, id)
	if err != nil {
		t.Fatalf("get initial: %v", err)
	}
	if state != metadata.MetadataStateClean {
		t.Fatalf("initial state=%q want clean", state)
	}
	if lastErr != "" {
		t.Fatalf("initial lastErr=%q want empty", lastErr)
	}
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		if err := r.SetMetadataState(ctx, tx, id, metadata.MetadataStateDirty); err != nil {
			return err
		}
		return r.SetLastRegenError(ctx, tx, id, "boom")
	}); err != nil {
		t.Fatalf("set dirty+err: %v", err)
	}
	state, lastErr, _ = r.GetMetadataState(ctx, id)
	if state != metadata.MetadataStateDirty || lastErr != "boom" {
		t.Fatalf("after set: state=%q lastErr=%q", state, lastErr)
	}
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		return r.SetMetadataState(ctx, tx, id, metadata.MetadataStateRegenerating)
	}); err != nil {
		t.Fatalf("set regenerating: %v", err)
	}
	state, _, _ = r.GetMetadataState(ctx, id)
	if state != metadata.MetadataStateRegenerating {
		t.Fatalf("state=%q want regenerating", state)
	}
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		if err := r.SetMetadataState(ctx, tx, id, metadata.MetadataStateClean); err != nil {
			return err
		}
		return r.SetLastRegenError(ctx, tx, id, "")
	}); err != nil {
		t.Fatalf("set clean: %v", err)
	}
	state, lastErr, _ = r.GetMetadataState(ctx, id)
	if state != metadata.MetadataStateClean || lastErr != "" {
		t.Fatalf("after success: state=%q lastErr=%q", state, lastErr)
	}
}

func TestReposMetadataStateCheckConstraint(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()
	pid := seedProject(t, db, "p-chk")
	r := metadata.NewReposRepo(db)
	id, _ := r.Create(ctx, pid, "rpm", "r-chk", "", nil, nil, nil)
	err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		return r.SetMetadataState(ctx, tx, id, "bogus")
	})
	if err == nil || !strings.Contains(err.Error(), "CHECK") {
		t.Fatalf("expected CHECK violation, got %v", err)
	}
}

func TestReposMetadataStateMissing(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	r := metadata.NewReposRepo(db)
	_, _, err := r.GetMetadataState(context.Background(), 9999)
	if !errors.Is(err, metadata.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

// --------------------------------------------------------------------------
// Phase 8 Plan 01 (MIRROR-01..07) — upstream-mirror columns round-trip.
// --------------------------------------------------------------------------

// TestReposRepo_MirrorColumns_RoundTrip asserts that creating a deb repo as a
// mirror via Create + SetMirrorConfigInTx round-trips every new mirror column
// via FindByTriple and ListByProject. Guards D-13 / D-14 / migration 024.
func TestReposRepo_MirrorColumns_RoundTrip(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()
	pid := seedProject(t, db, "p")
	r := metadata.NewReposRepo(db)

	id, err := r.Create(ctx, pid, "deb", "ubuntu-focal", "", nil, nil, nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	filter := `{"Suites":["focal"],"Components":["main"],"Arches":["amd64"]}`
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		return r.SetMirrorConfigInTx(ctx, tx, id, metadata.MirrorConfig{
			IsMirror:    true,
			UpstreamURL: "https://archive.ubuntu.com/ubuntu",
			FilterJSON:  filter,
			CredID:      nil,
			ScanOnSync:  true,
		})
	}); err != nil {
		t.Fatalf("set mirror config: %v", err)
	}

	got, err := r.FindByTriple(ctx, pid, "deb", "ubuntu-focal")
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if !got.IsMirror {
		t.Errorf("IsMirror = false, want true")
	}
	if got.MirrorUpstreamURL != "https://archive.ubuntu.com/ubuntu" {
		t.Errorf("MirrorUpstreamURL = %q", got.MirrorUpstreamURL)
	}
	if got.MirrorFilterJSON != filter {
		t.Errorf("MirrorFilterJSON = %q, want %q", got.MirrorFilterJSON, filter)
	}
	if got.MirrorCredID != nil {
		t.Errorf("MirrorCredID = %v, want nil", *got.MirrorCredID)
	}
	if !got.ScanOnSync {
		t.Errorf("ScanOnSync = false, want true")
	}

	// List should see the same values.
	list, err := r.ListByProject(ctx, pid)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("list: want 1, got %d", len(list))
	}
	if !list[0].IsMirror || list[0].MirrorUpstreamURL != "https://archive.ubuntu.com/ubuntu" {
		t.Errorf("list round-trip drift: %+v", list[0])
	}
}

// TestReposRepo_NonMirror_NullMirrorFields asserts default values for a
// non-mirror repo — every mirror column null / false / empty.
func TestReposRepo_NonMirror_NullMirrorFields(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()
	pid := seedProject(t, db, "p")
	r := metadata.NewReposRepo(db)

	_, err := r.Create(ctx, pid, "docker", "plain", "", nil, nil, nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := r.FindByTriple(ctx, pid, "docker", "plain")
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if got.IsMirror {
		t.Errorf("IsMirror = true, want false")
	}
	if got.MirrorUpstreamURL != "" {
		t.Errorf("MirrorUpstreamURL = %q, want empty", got.MirrorUpstreamURL)
	}
	if got.MirrorFilterJSON != "" {
		t.Errorf("MirrorFilterJSON = %q, want empty", got.MirrorFilterJSON)
	}
	if got.MirrorCredID != nil {
		t.Errorf("MirrorCredID = %v, want nil", got.MirrorCredID)
	}
	if got.ScanOnSync {
		t.Errorf("ScanOnSync = true, want false")
	}
}

// TestReposRepo_Update_MirrorFields asserts Update edits the 3 editable
// mirror fields and leaves is_mirror + mirror_upstream_url untouched.
func TestReposRepo_Update_MirrorFields(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()
	pid := seedProject(t, db, "p")
	r := metadata.NewReposRepo(db)

	id, err := r.Create(ctx, pid, "helm", "charts", "", nil, nil, nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		return r.SetMirrorConfigInTx(ctx, tx, id, metadata.MirrorConfig{
			IsMirror:    true,
			UpstreamURL: "https://charts.example/helm",
			FilterJSON:  `{"Names":["foo"]}`,
			CredID:      nil,
			ScanOnSync:  false,
		})
	}); err != nil {
		t.Fatalf("set mirror config: %v", err)
	}

	newFilter := `{"Names":["foo","bar"]}`
	scanOn := true
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := r.Update(ctx, tx, id, metadata.UpdateFields{
			MirrorFilterJSON: &newFilter,
			ScanOnSync:       &scanOn,
		})
		return err
	}); err != nil {
		t.Fatalf("update: %v", err)
	}

	got, err := r.FindByID(ctx, id)
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if got.MirrorFilterJSON != newFilter {
		t.Errorf("filter = %q, want %q", got.MirrorFilterJSON, newFilter)
	}
	if !got.ScanOnSync {
		t.Errorf("ScanOnSync = false, want true")
	}
	// Immutable fields unchanged.
	if !got.IsMirror {
		t.Errorf("IsMirror flipped false")
	}
	if got.MirrorUpstreamURL != "https://charts.example/helm" {
		t.Errorf("URL mutated: %q", got.MirrorUpstreamURL)
	}
}
