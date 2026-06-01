package driftpurge_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/vladoportos/omnirepo/internal/driftpurge"
	"github.com/vladoportos/omnirepo/internal/metadata"
	"github.com/vladoportos/omnirepo/internal/metadata/sqlitetest"
	"github.com/vladoportos/omnirepo/internal/storage"
)

// runDriftAdapter calls driftpurge.Run inside a real WriteTx and returns
// the report. Test helper kept terse so the four per-protocol tests
// stay readable.
func runDriftAdapter(t *testing.T, db *metadata.DB, repoID int64, actor string, adapter driftpurge.DriftAdapter) driftpurge.DriftReport {
	t.Helper()
	ctx := context.Background()
	var report driftpurge.DriftReport
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		var rerr error
		// thresholdPct=0 disables the percent-threshold guard so the
		// per-protocol adapter tests focus on adapter mechanics.
		report, rerr = driftpurge.Run(ctx, tx, repoID, actor, adapter, 0, false)
		return rerr
	}); err != nil {
		t.Fatalf("driftpurge.Run: %v", err)
	}
	return report
}

// findTrashEntry returns the first trash entry with the given kind (or
// fails the test). Drift round-trips expect exactly one drifted row, so
// "first" matches "only".
func findTrashEntry(t *testing.T, trash storage.Trash, wantKind string) storage.TrashEntry {
	t.Helper()
	entries, err := trash.List(context.Background())
	if err != nil {
		t.Fatalf("trash.List: %v", err)
	}
	var hits []storage.TrashEntry
	for _, e := range entries {
		if e.Kind == wantKind {
			hits = append(hits, e)
		}
	}
	if len(hits) != 1 {
		t.Fatalf("trash entries with kind=%q: got %d, want 1 (all=%v)", wantKind, len(hits), entries)
	}
	return hits[0]
}

// =============================================================================
// PyPI
// =============================================================================

func TestAdapter_PyPI_DriftRoundTrip(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()

	if _, err := db.Writer.ExecContext(ctx, `INSERT INTO projects(name) VALUES ('p1')`); err != nil {
		t.Fatalf("project: %v", err)
	}
	res, err := db.Writer.ExecContext(ctx,
		`INSERT INTO repos(project_id,type,name) VALUES (1,'pypi','r1')`,
	)
	if err != nil {
		t.Fatalf("repo: %v", err)
	}
	repoID, _ := res.LastInsertId()

	pypiFiles := metadata.NewPyPIFilesRepo(db)
	seed := []*metadata.PyPIFile{
		{RepoID: repoID, ProjectNormalized: "foo", Version: "1.0.0", Filename: "foo-1.0.0.tar.gz", Kind: "sdist", Digest: "sha256:a"},
		{RepoID: repoID, ProjectNormalized: "foo", Version: "1.0.1", Filename: "foo-1.0.1.tar.gz", Kind: "sdist", Digest: "sha256:b"},
		{RepoID: repoID, ProjectNormalized: "bar", Version: "2.0.0", Filename: "bar-2.0.0.tar.gz", Kind: "sdist", Digest: "sha256:c"},
	}
	for _, f := range seed {
		if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
			_, ierr := pypiFiles.Insert(ctx, tx, f)
			return ierr
		}); err != nil {
			t.Fatalf("seed %s: %v", f.Filename, err)
		}
	}

	trashRoot := filepath.Join(t.TempDir(), "trash")
	trash := storage.NewTrash(trashRoot)

	// Upstream keeps foo/1.0.0 and bar/2.0.0; foo/1.0.1 is drift.
	upstream := []driftpurge.Key{
		{A: "foo", B: "foo-1.0.0.tar.gz", C: "sha256:a"},
		{A: "bar", B: "bar-2.0.0.tar.gz", C: "sha256:c"},
	}
	pathFn := func(row *metadata.PyPIFile) string {
		// Intentionally non-existent path — Trash.MoveWithSnapshot tolerates
		// missing source via os.ErrNotExist short-circuit in Purge.
		return filepath.Join(t.TempDir(), row.Filename)
	}
	adapter := driftpurge.NewPyPIAdapter(upstream, pypiFiles, trash, pathFn)

	report := runDriftAdapter(t, db, repoID, "tester", adapter)
	if report.PurgedCount != 1 {
		t.Errorf("PurgedCount = %d, want 1", report.PurgedCount)
	}
	if report.Protocol != "pypi" {
		t.Errorf("Protocol = %q, want pypi", report.Protocol)
	}
	if len(report.Sample) != 1 || report.Sample[0] != "foo-1.0.1.tar.gz" {
		t.Errorf("Sample = %v, want [foo-1.0.1.tar.gz]", report.Sample)
	}

	entry := findTrashEntry(t, trash, "pypi_file_drift")
	if entry.RowSnapshot == nil {
		t.Fatal("RowSnapshot nil; want populated by adapter")
	}
	var snap map[string]any
	if err := json.Unmarshal(entry.RowSnapshot, &snap); err != nil {
		t.Fatalf("unmarshal snapshot: %v", err)
	}
	if snap["filename"] != "foo-1.0.1.tar.gz" {
		t.Errorf("snapshot.filename = %v, want foo-1.0.1.tar.gz", snap["filename"])
	}
	if snap["digest"] != "sha256:b" {
		t.Errorf("snapshot.digest = %v, want sha256:b", snap["digest"])
	}

	remaining, err := pypiFiles.ListByRepo(ctx, repoID)
	if err != nil {
		t.Fatalf("ListByRepo: %v", err)
	}
	if len(remaining) != 2 {
		t.Errorf("rows after drift = %d, want 2", len(remaining))
	}
	for _, r := range remaining {
		if r.Filename == "foo-1.0.1.tar.gz" {
			t.Errorf("drifted row foo-1.0.1.tar.gz should be deleted but is still present")
		}
	}
}

// =============================================================================
// RPM
// =============================================================================

func TestAdapter_RPM_DriftRoundTrip(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()

	if _, err := db.Writer.ExecContext(ctx, `INSERT INTO projects(name) VALUES ('p1')`); err != nil {
		t.Fatalf("project: %v", err)
	}
	res, err := db.Writer.ExecContext(ctx,
		`INSERT INTO repos(project_id,type,name) VALUES (1,'rpm','r1')`,
	)
	if err != nil {
		t.Fatalf("repo: %v", err)
	}
	repoID, _ := res.LastInsertId()

	rpms := metadata.NewRPMPackagesRepo(db)
	seed := []*metadata.RPMPackage{
		{RepoID: repoID, Name: "alpha", Version: "1.0", Release: "1", Arch: "x86_64", Digest: "sha256:a", Filename: "alpha-1.0-1.x86_64.rpm"},
		{RepoID: repoID, Name: "beta", Version: "2.0", Release: "1", Arch: "x86_64", Digest: "sha256:b", Filename: "beta-2.0-1.x86_64.rpm"},
		{RepoID: repoID, Name: "gamma", Version: "3.0", Release: "1", Arch: "x86_64", Digest: "sha256:c", Filename: "gamma-3.0-1.x86_64.rpm"},
	}
	for _, p := range seed {
		if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
			_, ierr := rpms.Insert(ctx, tx, p)
			return ierr
		}); err != nil {
			t.Fatalf("seed %s: %v", p.Filename, err)
		}
	}

	trashRoot := filepath.Join(t.TempDir(), "trash")
	trash := storage.NewTrash(trashRoot)

	// Upstream keeps alpha + gamma; beta is drift. Key: {name,version,arch}.
	upstream := []driftpurge.Key{
		{A: "alpha", B: "1.0", C: "x86_64"},
		{A: "gamma", B: "3.0", C: "x86_64"},
	}
	pathFn := func(row *metadata.RPMPackage) string {
		return filepath.Join(t.TempDir(), row.Filename)
	}
	adapter := driftpurge.NewRPMAdapter(upstream, rpms, trash, pathFn)

	report := runDriftAdapter(t, db, repoID, "tester", adapter)
	if report.PurgedCount != 1 {
		t.Errorf("PurgedCount = %d, want 1", report.PurgedCount)
	}
	if report.Protocol != "rpm" {
		t.Errorf("Protocol = %q, want rpm", report.Protocol)
	}
	if len(report.Sample) != 1 || report.Sample[0] != "beta-2.0-1.x86_64.rpm" {
		t.Errorf("Sample = %v, want [beta-2.0-1.x86_64.rpm]", report.Sample)
	}

	entry := findTrashEntry(t, trash, "rpm_package_drift")
	if entry.RowSnapshot == nil {
		t.Fatal("RowSnapshot nil")
	}
	var snap map[string]any
	if err := json.Unmarshal(entry.RowSnapshot, &snap); err != nil {
		t.Fatalf("unmarshal snapshot: %v", err)
	}
	if snap["name"] != "beta" {
		t.Errorf("snapshot.name = %v, want beta", snap["name"])
	}

	remaining, err := rpms.ListByRepo(ctx, repoID)
	if err != nil {
		t.Fatalf("ListByRepo: %v", err)
	}
	if len(remaining) != 2 {
		t.Errorf("rows after drift = %d, want 2", len(remaining))
	}
	for _, r := range remaining {
		if r.Name == "beta" {
			t.Errorf("drifted row beta should be deleted")
		}
	}
}

// =============================================================================
// DEB
// =============================================================================

func TestAdapter_DEB_DriftRoundTrip(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()

	if _, err := db.Writer.ExecContext(ctx, `INSERT INTO projects(name) VALUES ('p1')`); err != nil {
		t.Fatalf("project: %v", err)
	}
	res, err := db.Writer.ExecContext(ctx,
		`INSERT INTO repos(project_id,type,name) VALUES (1,'deb','r1')`,
	)
	if err != nil {
		t.Fatalf("repo: %v", err)
	}
	repoID, _ := res.LastInsertId()

	suitesRepo := metadata.NewAptSuitesRepo(db)
	debs := metadata.NewDEBPackagesRepo(db)

	// One suite (stable, main, amd64) -> suite_id; three packages.
	var suiteID int64
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		v, ierr := suitesRepo.Insert(ctx, tx, repoID, "stable", "main", "amd64")
		suiteID = v
		return ierr
	}); err != nil {
		t.Fatalf("seed suite: %v", err)
	}

	seed := []*metadata.DEBPackage{
		{RepoID: repoID, SuiteID: suiteID, Package: "alpha", Version: "1.0", Architecture: "amd64", Digest: "sha256:a", Filename: "alpha_1.0_amd64.deb"},
		{RepoID: repoID, SuiteID: suiteID, Package: "beta", Version: "2.0", Architecture: "amd64", Digest: "sha256:b", Filename: "beta_2.0_amd64.deb"},
		{RepoID: repoID, SuiteID: suiteID, Package: "gamma", Version: "3.0", Architecture: "amd64", Digest: "sha256:c", Filename: "gamma_3.0_amd64.deb"},
	}
	for _, p := range seed {
		if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
			_, ierr := debs.Insert(ctx, tx, p)
			return ierr
		}); err != nil {
			t.Fatalf("seed %s: %v", p.Package, err)
		}
	}

	trashRoot := filepath.Join(t.TempDir(), "trash")
	trash := storage.NewTrash(trashRoot)

	// Drift key: Key{A: name+"|"+component+"|"+suite, B: version, C: arch}.
	// Upstream keeps alpha + gamma; beta is drift.
	upstream := []driftpurge.Key{
		{A: "alpha|main|stable", B: "1.0", C: "amd64"},
		{A: "gamma|main|stable", B: "3.0", C: "amd64"},
	}
	pathFn := func(row *metadata.DEBPackage) string {
		return filepath.Join(t.TempDir(), row.Filename)
	}
	adapter := driftpurge.NewDEBAdapter(upstream, []string{"stable"}, debs, suitesRepo, trash, pathFn)

	report := runDriftAdapter(t, db, repoID, "tester", adapter)
	if report.PurgedCount != 1 {
		t.Errorf("PurgedCount = %d, want 1", report.PurgedCount)
	}
	if report.Protocol != "deb" {
		t.Errorf("Protocol = %q, want deb", report.Protocol)
	}
	if len(report.Sample) != 1 || report.Sample[0] != "beta_2.0_amd64.deb" {
		t.Errorf("Sample = %v, want [beta_2.0_amd64.deb]", report.Sample)
	}

	entry := findTrashEntry(t, trash, "deb_package_drift")
	if entry.RowSnapshot == nil {
		t.Fatal("RowSnapshot nil")
	}
	var snap map[string]any
	if err := json.Unmarshal(entry.RowSnapshot, &snap); err != nil {
		t.Fatalf("unmarshal snapshot: %v", err)
	}
	if snap["package"] != "beta" {
		t.Errorf("snapshot.package = %v, want beta", snap["package"])
	}

	remaining, err := debs.ListByRepo(ctx, repoID)
	if err != nil {
		t.Fatalf("ListByRepo: %v", err)
	}
	if len(remaining) != 2 {
		t.Errorf("rows after drift = %d, want 2", len(remaining))
	}
	for _, r := range remaining {
		if r.Package == "beta" {
			t.Errorf("drifted row beta should be deleted")
		}
	}
}

// TestAdapter_DEB_DriftScopedToSyncedSuites is the regression test: a sync that
// processes only some suites must NOT purge packages in the suites it did not
// touch. Previously LocalRows returned every row across all suites, so a
// per-suite sync classified unrelated suites as drift and wiped them.
func TestAdapter_DEB_DriftScopedToSyncedSuites(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()

	if _, err := db.Writer.ExecContext(ctx, `INSERT INTO projects(name) VALUES ('p1')`); err != nil {
		t.Fatalf("project: %v", err)
	}
	res, err := db.Writer.ExecContext(ctx, `INSERT INTO repos(project_id,type,name) VALUES (1,'deb','r1')`)
	if err != nil {
		t.Fatalf("repo: %v", err)
	}
	repoID, _ := res.LastInsertId()

	suitesRepo := metadata.NewAptSuitesRepo(db)
	debs := metadata.NewDEBPackagesRepo(db)

	var jammyID, focalID int64
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		var ierr error
		if jammyID, ierr = suitesRepo.Insert(ctx, tx, repoID, "jammy", "main", "amd64"); ierr != nil {
			return ierr
		}
		focalID, ierr = suitesRepo.Insert(ctx, tx, repoID, "focal", "main", "amd64")
		return ierr
	}); err != nil {
		t.Fatalf("seed suites: %v", err)
	}

	seed := []*metadata.DEBPackage{
		{RepoID: repoID, SuiteID: jammyID, Package: "alpha", Version: "1.0", Architecture: "amd64", Digest: "sha256:a", Filename: "alpha_1.0_amd64.deb"},
		{RepoID: repoID, SuiteID: jammyID, Package: "beta", Version: "2.0", Architecture: "amd64", Digest: "sha256:b", Filename: "beta_2.0_amd64.deb"},
		{RepoID: repoID, SuiteID: focalID, Package: "gamma", Version: "3.0", Architecture: "amd64", Digest: "sha256:c", Filename: "gamma_3.0_amd64.deb"},
		{RepoID: repoID, SuiteID: focalID, Package: "delta", Version: "4.0", Architecture: "amd64", Digest: "sha256:d", Filename: "delta_4.0_amd64.deb"},
	}
	for _, p := range seed {
		if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
			_, ierr := debs.Insert(ctx, tx, p)
			return ierr
		}); err != nil {
			t.Fatalf("seed %s: %v", p.Package, err)
		}
	}

	trash := storage.NewTrash(filepath.Join(t.TempDir(), "trash"))
	pathFn := func(row *metadata.DEBPackage) string { return filepath.Join(t.TempDir(), row.Filename) }

	// Sync ONLY jammy; upstream jammy keeps alpha (beta is drift). focal is not
	// synced, so upstreamKeys contains no focal entries.
	upstream := []driftpurge.Key{{A: "alpha|main|jammy", B: "1.0", C: "amd64"}}
	adapter := driftpurge.NewDEBAdapter(upstream, []string{"jammy"}, debs, suitesRepo, trash, pathFn)

	report := runDriftAdapter(t, db, repoID, "tester", adapter)

	// Only beta (synced suite, dropped upstream) should be purged; gamma/delta
	// live in the un-synced focal suite and MUST survive.
	if report.PurgedCount != 1 {
		t.Fatalf("PurgedCount = %d, want 1 (only jammy/beta)", report.PurgedCount)
	}
	remaining, err := debs.ListByRepo(ctx, repoID)
	if err != nil {
		t.Fatalf("ListByRepo: %v", err)
	}
	got := map[string]bool{}
	for _, r := range remaining {
		got[r.Package] = true
	}
	if got["beta"] {
		t.Error("jammy/beta should have been purged")
	}
	for _, p := range []string{"alpha", "gamma", "delta"} {
		if !got[p] {
			t.Errorf("%s should have survived (alpha kept upstream; gamma/delta in un-synced focal)", p)
		}
	}
	if len(remaining) != 3 {
		t.Fatalf("rows after drift = %d, want 3", len(remaining))
	}
}

// =============================================================================
// Helm
// =============================================================================

func TestAdapter_Helm_DriftRoundTrip(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()

	if _, err := db.Writer.ExecContext(ctx, `INSERT INTO projects(name) VALUES ('p1')`); err != nil {
		t.Fatalf("project: %v", err)
	}
	res, err := db.Writer.ExecContext(ctx,
		`INSERT INTO repos(project_id,type,name) VALUES (1,'helm','r1')`,
	)
	if err != nil {
		t.Fatalf("repo: %v", err)
	}
	repoID, _ := res.LastInsertId()

	charts := metadata.NewHelmChartsRepo(db)
	seed := []*metadata.HelmChart{
		{RepoID: repoID, Name: "alpha", Version: "1.0.0", Digest: "sha256:a", Filename: "alpha-1.0.0.tgz"},
		{RepoID: repoID, Name: "beta", Version: "2.0.0", Digest: "sha256:b", Filename: "beta-2.0.0.tgz"},
		{RepoID: repoID, Name: "gamma", Version: "3.0.0", Digest: "sha256:c", Filename: "gamma-3.0.0.tgz"},
	}
	for _, c := range seed {
		if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
			_, ierr := charts.Insert(ctx, tx, c)
			return ierr
		}); err != nil {
			t.Fatalf("seed %s: %v", c.Name, err)
		}
	}

	trashRoot := filepath.Join(t.TempDir(), "trash")
	trash := storage.NewTrash(trashRoot)

	// Upstream keeps alpha + gamma; beta is drift. Key: {name,version,""}.
	upstream := []driftpurge.Key{
		{A: "alpha", B: "1.0.0", C: ""},
		{A: "gamma", B: "3.0.0", C: ""},
	}
	pathFn := func(row *metadata.HelmChart) string {
		return filepath.Join(t.TempDir(), row.Filename)
	}
	adapter := driftpurge.NewHelmAdapter(upstream, charts, trash, pathFn)

	report := runDriftAdapter(t, db, repoID, "tester", adapter)
	if report.PurgedCount != 1 {
		t.Errorf("PurgedCount = %d, want 1", report.PurgedCount)
	}
	if report.Protocol != "helm" {
		t.Errorf("Protocol = %q, want helm", report.Protocol)
	}
	if len(report.Sample) != 1 || report.Sample[0] != "beta-2.0.0.tgz" {
		t.Errorf("Sample = %v, want [beta-2.0.0.tgz]", report.Sample)
	}

	entry := findTrashEntry(t, trash, "helm_chart_drift")
	if entry.RowSnapshot == nil {
		t.Fatal("RowSnapshot nil")
	}
	var snap map[string]any
	if err := json.Unmarshal(entry.RowSnapshot, &snap); err != nil {
		t.Fatalf("unmarshal snapshot: %v", err)
	}
	if snap["name"] != "beta" {
		t.Errorf("snapshot.name = %v, want beta", snap["name"])
	}

	remaining, err := charts.ListByRepo(ctx, repoID)
	if err != nil {
		t.Fatalf("ListByRepo: %v", err)
	}
	if len(remaining) != 2 {
		t.Errorf("rows after drift = %d, want 2", len(remaining))
	}
	for _, r := range remaining {
		if r.Name == "beta" {
			t.Errorf("drifted row beta should be deleted")
		}
	}
}
