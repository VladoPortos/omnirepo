package helm_test

// Phase 11 Plan 03 — OCI pull integration tests.
//
// Replaces the v1.2 skipped_oci_entries stub behavior (see
// TestHelmSync_SkipsOCIEntries in sync_progress_test.go) with end-to-end
// coverage for the real OCI fetch path: dedup on
// (repo_id, name, version, digest), tag-rebound via Trash.Move +
// EvtOciTagRebound audit, and HTTP entries unchanged in mixed indexes.
//
// Hermetic — uses ociclient.FakeClient + a local httptest.Server hosting a
// synthetic index.yaml. No Docker Hub or registry network traffic.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	helmrepo "helm.sh/helm/v3/pkg/repo"

	"github.com/dxc-internal/omnirepo/internal/audit"
	"github.com/dxc-internal/omnirepo/internal/config"
	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/metadata/sqlitetest"
	"github.com/dxc-internal/omnirepo/internal/protocol/helm"
	"github.com/dxc-internal/omnirepo/internal/protocol/helm/ociclient"
	"github.com/dxc-internal/omnirepo/internal/storage"
)

// recordingAudit captures audit.Record calls so tests can assert kinds +
// details without a real DB-backed audit sink.
type recordingAudit struct {
	mu     sync.Mutex
	events []audit.Event
}

func (r *recordingAudit) Record(_ context.Context, e audit.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
	return nil
}

func (r *recordingAudit) find(kind audit.EventKind) []audit.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []audit.Event
	for _, e := range r.events {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

// ociHelmFixture bundles a Helm SyncHandler wired with a FakeClient and a
// local httptest upstream serving index.yaml. Callers populate the
// FakeClient's canned PullResults BEFORE invoking runSync.
type ociHelmFixture struct {
	t             *testing.T
	h             *helm.SyncHandler
	db            *metadata.DB
	helmCharts    *metadata.HelmChartsRepo
	repoID        int64
	projName      string
	repoName      string
	fakeOCI       *ociclient.FakeClient
	auditRecorder *recordingAudit
	upstreamURL   string
	upstreamSrv   *httptest.Server
	repoRoot      string
}

// newOCIHelmFixture builds a helm SyncHandler whose upstream index.yaml is
// served by a local httptest server. The caller supplies indexBody plus any
// http tgz chart payloads via the srv mux (use http.ServeMux); this helper
// does the plumbing.
func newOCIHelmFixture(t *testing.T, indexBody string, httpCharts map[string][]byte) *ociHelmFixture {
	t.Helper()

	db := sqlitetest.New(t)
	reposRepo := metadata.NewReposRepo(db)
	projectsRepo := metadata.NewProjectsRepo(db)
	helmCharts := metadata.NewHelmChartsRepo(db)
	scans := metadata.NewScansRepo(db)

	mux := http.NewServeMux()
	mux.HandleFunc("/index.yaml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-yaml")
		_, _ = w.Write([]byte(indexBody))
	})
	for path, body := range httpCharts {
		b := body
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/gzip")
			_, _ = w.Write(b)
		})
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	ctx := context.Background()
	projName := "p1"
	repoName := "r1"
	pid, err := projectsRepo.Create(ctx, projName, "oci helm sync integration")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	rid, err := reposRepo.Create(ctx, pid, "helm", repoName, "", nil, nil, nil)
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}

	dataRoot := t.TempDir()
	repoRoot := filepath.Join(dataRoot, "repos")
	trashRoot := filepath.Join(dataRoot, "trash")
	if err := os.MkdirAll(trashRoot, 0o750); err != nil {
		t.Fatalf("mkdir trash: %v", err)
	}
	pathStore := storage.NewPathStore(repoRoot)

	fakeOCI := ociclient.NewFake()
	rec := &recordingAudit{}

	h := helm.NewSyncHandler(helm.SyncDeps{
		DB:         db,
		Path:       pathStore,
		HelmCharts: helmCharts,
		Repos:      reposRepo,
		Projects:   projectsRepo,
		Scans:      scans,
		Audit:      rec,
		Coalescer:  nil,
		HTTPClient: srv.Client(),
		RepoRoot:   repoRoot,
		Cfg:        config.SyncConfig{MaxParallelDownloadsPerJob: 1},
		SyncJobs:   metadata.NewSyncJobsRepo(db),
		OCIClient:  fakeOCI,
		Trash:      storage.NewTrash(trashRoot),
	})

	return &ociHelmFixture{
		t:             t,
		h:             h,
		db:            db,
		helmCharts:    helmCharts,
		repoID:        rid,
		projName:      projName,
		repoName:      repoName,
		fakeOCI:       fakeOCI,
		auditRecorder: rec,
		upstreamURL:   srv.URL,
		upstreamSrv:   srv,
		repoRoot:      repoRoot,
	}
}

func (f *ociHelmFixture) runSync(t *testing.T) error {
	t.Helper()
	jobID := seedHelmSyncJobRow(t, f.db, f.repoID)
	payload := map[string]string{"upstream_url": f.upstreamURL}
	pb, _ := json.Marshal(payload)
	return f.h.Handle(context.Background(), string(pb), 0, f.repoID, jobID)
}

// buildOCIChartTGZ wraps makeChartTGZ (from testutil_test.go) so the OCI tests
// share a single chart-archive fixture shape.
func buildOCIChartTGZ(t *testing.T, name, version string) []byte {
	t.Helper()
	return makeChartTGZ(t, name, version, version, name+" chart", nil)
}

// sha256OfBytes is a small helper that returns the "sha256:<hex>" digest
// of b in the same shape helm_charts.digest stores.
func sha256OfBytes(b []byte) string {
	return "sha256:" + shaHex(b)
}

// makeOCIIndex returns an index.yaml body listing chart entries with the
// supplied OCI-style URLs. The digest fields are zero'd because oci entries
// in real upstreams often omit them (the pull path verifies via the
// manifest digest).
func makeOCIIndex(entries []ociIndexEntry) string {
	var sb strings.Builder
	sb.WriteString("apiVersion: v1\nentries:\n")
	// Group by chart name.
	byName := map[string][]ociIndexEntry{}
	order := []string{}
	for _, e := range entries {
		if _, ok := byName[e.Name]; !ok {
			order = append(order, e.Name)
		}
		byName[e.Name] = append(byName[e.Name], e)
	}
	for _, name := range order {
		sb.WriteString("  " + name + ":\n")
		for _, e := range byName[name] {
			sb.WriteString("    - apiVersion: v2\n")
			sb.WriteString("      name: " + e.Name + "\n")
			sb.WriteString("      version: " + e.Version + "\n")
			sb.WriteString("      appVersion: \"" + e.Version + "\"\n")
			sb.WriteString("      description: " + e.Name + " chart\n")
			if e.Digest != "" {
				sb.WriteString("      digest: " + e.Digest + "\n")
			} else {
				// LoadIndexFile tolerates missing digest but emits a warning
				// to stderr; populate with a dummy sha hex to silence.
				sb.WriteString("      digest: 0000000000000000000000000000000000000000000000000000000000000000\n")
			}
			sb.WriteString("      urls:\n")
			sb.WriteString("        - " + e.URL + "\n")
		}
	}
	sb.WriteString("generated: \"2026-04-22T00:00:00Z\"\n")
	return sb.String()
}

type ociIndexEntry struct {
	Name, Version, URL, Digest string
}

// TestOCISync_SingleChart — one OCI entry in a Helm upstream index.yaml.
// FakeClient returns canned chart bytes; sync completes; helm_charts row
// is inserted with the chart-layer digest; no skipped_oci_entries > 0
// appears in audit progress events.
func TestOCISync_SingleChart(t *testing.T) {
	chart := buildOCIChartTGZ(t, "nginx", "1.0.0")
	chartDigest := sha256OfBytes(chart)

	ociRef := "registry-1.docker.io/bitnamicharts/nginx:1.0.0"
	index := makeOCIIndex([]ociIndexEntry{
		{Name: "nginx", Version: "1.0.0", URL: "oci://" + ociRef},
	})

	f := newOCIHelmFixture(t, index, nil)
	f.fakeOCI.Results[ociRef] = &ociclient.PullResult{
		Data:   chart,
		Digest: chartDigest,
		Size:   int64(len(chart)),
		Meta: ociclient.ChartMeta{
			Name:    "nginx",
			Version: "1.0.0",
		},
	}

	if err := f.runSync(t); err != nil {
		t.Fatalf("sync: %v", err)
	}

	ctx := context.Background()
	rows, err := f.helmCharts.ListByRepo(ctx, f.repoID)
	if err != nil {
		t.Fatalf("list charts: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("helm_charts rows = %d; want 1", len(rows))
	}
	row := rows[0]
	if row.Name != "nginx" || row.Version != "1.0.0" {
		t.Errorf("row name/version = (%q,%q); want (nginx,1.0.0)", row.Name, row.Version)
	}
	if row.Digest != chartDigest {
		t.Errorf("row digest = %q; want %q", row.Digest, chartDigest)
	}

	// The on-disk chart tgz must live under <repoRoot>/<proj>/helm/<repo>/charts/.
	wantPath := filepath.Join(f.repoRoot, f.projName, "helm", f.repoName, "charts", "nginx-1.0.0.tgz")
	if _, err := os.Stat(wantPath); err != nil {
		t.Errorf("stat on-disk chart: %v", err)
	}

	// Audit MUST NOT contain a positive skipped_oci_entries value.
	for _, ev := range f.auditRecorder.find(audit.EvtSyncProgress) {
		if v, ok := ev.Details["skipped_oci_entries"].(int); ok && v > 0 {
			t.Errorf("unexpected skipped_oci_entries=%d in audit progress", v)
		}
	}

	// FakeClient should have seen at least one Resolve + one PullChart for ociRef.
	sawResolve, sawPull := false, false
	for _, c := range f.fakeOCI.Calls {
		if c == "Resolve:"+ociRef {
			sawResolve = true
		}
		if c == "PullChart:"+ociRef {
			sawPull = true
		}
	}
	if !sawResolve {
		t.Errorf("FakeClient Calls = %v; want a Resolve for %s", f.fakeOCI.Calls, ociRef)
	}
	if !sawPull {
		t.Errorf("FakeClient Calls = %v; want a PullChart for %s", f.fakeOCI.Calls, ociRef)
	}
}

// TestOCISync_DedupSkipsPull — a second sync over the same (name, version,
// digest) short-circuits: Resolve is still invoked (pre-flight dedup
// check) but PullChart is NOT — no bandwidth wasted.
func TestOCISync_DedupSkipsPull(t *testing.T) {
	chart := buildOCIChartTGZ(t, "redis", "7.0.0")
	chartDigest := sha256OfBytes(chart)

	ociRef := "registry-1.docker.io/bitnamicharts/redis:7.0.0"
	index := makeOCIIndex([]ociIndexEntry{
		{Name: "redis", Version: "7.0.0", URL: "oci://" + ociRef},
	})

	f := newOCIHelmFixture(t, index, nil)
	f.fakeOCI.Results[ociRef] = &ociclient.PullResult{
		Data:   chart,
		Digest: chartDigest,
		Size:   int64(len(chart)),
		Meta:   ociclient.ChartMeta{Name: "redis", Version: "7.0.0"},
	}

	// First sync — lands the chart.
	if err := f.runSync(t); err != nil {
		t.Fatalf("first sync: %v", err)
	}
	// Count PullChart calls so far.
	firstPulls := 0
	for _, c := range f.fakeOCI.Calls {
		if c == "PullChart:"+ociRef {
			firstPulls++
		}
	}
	if firstPulls != 1 {
		t.Fatalf("first sync PullChart count = %d; want 1", firstPulls)
	}

	// Second sync with the same digest — dedup should short-circuit.
	if err := f.runSync(t); err != nil {
		t.Fatalf("second sync: %v", err)
	}
	totalPulls := 0
	totalResolves := 0
	for _, c := range f.fakeOCI.Calls {
		if c == "PullChart:"+ociRef {
			totalPulls++
		}
		if c == "Resolve:"+ociRef {
			totalResolves++
		}
	}
	if totalPulls != 1 {
		t.Errorf("after second sync PullChart count = %d; want 1 (dedup should skip)", totalPulls)
	}
	if totalResolves < 2 {
		t.Errorf("after second sync Resolve count = %d; want >=2 (pre-flight digest check)", totalResolves)
	}

	// helm_charts still has exactly one row.
	rows, _ := f.helmCharts.ListByRepo(context.Background(), f.repoID)
	if len(rows) != 1 {
		t.Errorf("helm_charts rows = %d; want 1 (idempotent)", len(rows))
	}
}

// TestOCISync_TagRebound — same (name, version) tag resolves to a new
// digest on the second sync. The old digest's on-disk file MUST be soft-
// deleted via Trash.Move with kind "oci_tag_rebound" (D-02), an
// EvtOciTagRebound audit event MUST fire with the full D-05 details_json
// shape, and the replacement helm_charts row MUST carry the new digest.
// The final state MUST have exactly one active row for (repo_id, name,
// version).
func TestOCISync_TagRebound(t *testing.T) {
	chartOld := buildOCIChartTGZ(t, "nginx", "1.2.3")
	oldDigest := sha256OfBytes(chartOld)
	chartNew := append([]byte{}, chartOld...)
	// Perturb a single tar-content byte so the new chart is a distinct
	// artifact with a distinct sha256 — the tar is gzipped so we cannot
	// simply flip a byte; rebuild with a tweaked description.
	chartNew = makeChartTGZ(t, "nginx", "1.2.3", "1.2.3", "nginx rebuilt chart", nil)
	newDigest := sha256OfBytes(chartNew)
	if oldDigest == newDigest {
		t.Fatalf("fixture invalid: old and new digests collide")
	}

	ociRef := "registry-1.docker.io/bitnamicharts/nginx:1.2.3"
	index := makeOCIIndex([]ociIndexEntry{
		{Name: "nginx", Version: "1.2.3", URL: "oci://" + ociRef},
	})

	f := newOCIHelmFixture(t, index, nil)
	// First sync — seed with the OLD digest.
	f.fakeOCI.Results[ociRef] = &ociclient.PullResult{
		Data:   chartOld,
		Digest: oldDigest,
		Size:   int64(len(chartOld)),
		Meta:   ociclient.ChartMeta{Name: "nginx", Version: "1.2.3"},
	}
	if err := f.runSync(t); err != nil {
		t.Fatalf("first sync: %v", err)
	}
	// Confirm the old row landed.
	ctx := context.Background()
	rows, _ := f.helmCharts.ListByRepo(ctx, f.repoID)
	if len(rows) != 1 || rows[0].Digest != oldDigest {
		t.Fatalf("after first sync rows=%+v; want 1 row with digest=%s", rows, oldDigest)
	}
	// Drop prior audit events so the tag-rebound assertion below only sees
	// the second-sync emissions.
	f.auditRecorder.mu.Lock()
	f.auditRecorder.events = nil
	f.auditRecorder.mu.Unlock()

	// Second sync — flip FakeClient to the NEW digest for the same tag.
	f.fakeOCI.Results[ociRef] = &ociclient.PullResult{
		Data:   chartNew,
		Digest: newDigest,
		Size:   int64(len(chartNew)),
		Meta:   ociclient.ChartMeta{Name: "nginx", Version: "1.2.3"},
	}
	if err := f.runSync(t); err != nil {
		t.Fatalf("second sync: %v", err)
	}

	// Post-rebound helm_charts state: exactly one row, with newDigest.
	rowsAfter, err := f.helmCharts.ListByRepo(ctx, f.repoID)
	if err != nil {
		t.Fatalf("list charts after rebound: %v", err)
	}
	if len(rowsAfter) != 1 {
		t.Fatalf("helm_charts rows after rebound = %d; want 1 (INV-11-03-01)", len(rowsAfter))
	}
	if rowsAfter[0].Digest != newDigest {
		t.Errorf("row digest after rebound = %q; want %q", rowsAfter[0].Digest, newDigest)
	}

	// Exactly one EvtOciTagRebound audit event, with the D-05 details shape.
	rebounds := f.auditRecorder.find(audit.EvtOciTagRebound)
	if len(rebounds) != 1 {
		t.Fatalf("EvtOciTagRebound events = %d; want 1", len(rebounds))
	}
	det := rebounds[0].Details
	if det == nil {
		t.Fatalf("rebound event Details is nil")
	}
	want := map[string]string{
		"name":       "nginx",
		"version":    "1.2.3",
		"old_digest": oldDigest,
		"new_digest": newDigest,
	}
	for k, v := range want {
		gotStr, _ := det[k].(string)
		if gotStr != v {
			t.Errorf("rebound details[%q] = %q; want %q", k, gotStr, v)
		}
	}
	if det["upstream_url"] == nil {
		t.Errorf("rebound details missing upstream_url")
	}
	if det["repo_id"] == nil {
		t.Errorf("rebound details missing repo_id")
	}
	if det["replaced_at"] == nil {
		t.Errorf("rebound details missing replaced_at")
	}

	// Trash must contain exactly one holder for the old chart, with
	// kind "oci_tag_rebound".
	trashRoot := filepath.Join(filepath.Dir(f.repoRoot), "trash")
	sawRebound := false
	_ = filepath.Walk(trashRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil {
			return nil
		}
		if info.IsDir() && strings.Contains(info.Name(), "-oci_tag_rebound-") {
			sawRebound = true
		}
		return nil
	})
	if !sawRebound {
		t.Errorf("expected a trash holder dir containing %q; walk found none under %s", "-oci_tag_rebound-", trashRoot)
	}
}

// TestOCISync_MixedIndex_HTTPEntriesUnchanged — an index.yaml lists one
// http-tgz entry AND one oci entry. The http path must still commit via
// the existing HTTP fetchAndCommit path (regression-free) and the oci
// path must commit via the new OCI branch. Final state: two helm_charts
// rows.
func TestOCISync_MixedIndex_HTTPEntriesUnchanged(t *testing.T) {
	chartHTTP := buildOCIChartTGZ(t, "nginx", "1.0.0")
	chartOCI := buildOCIChartTGZ(t, "redis", "7.0.0")
	ociRef := "registry-1.docker.io/bitnamicharts/redis:7.0.0"
	ociDigest := sha256OfBytes(chartOCI)

	index := "apiVersion: v1\nentries:\n" +
		"  nginx:\n" +
		"    - apiVersion: v2\n      name: nginx\n      version: 1.0.0\n" +
		"      appVersion: \"1.0.0\"\n      description: web server\n" +
		"      digest: " + shaHex(chartHTTP) + "\n" +
		"      urls:\n        - charts/nginx-1.0.0.tgz\n" +
		"  redis:\n" +
		"    - apiVersion: v2\n      name: redis\n      version: 7.0.0\n" +
		"      appVersion: \"7.0.0\"\n      description: kv store\n" +
		"      digest: 0000000000000000000000000000000000000000000000000000000000000000\n" +
		"      urls:\n        - oci://" + ociRef + "\n" +
		"generated: \"2026-04-22T00:00:00Z\"\n"

	f := newOCIHelmFixture(t, index, map[string][]byte{
		"/charts/nginx-1.0.0.tgz": chartHTTP,
	})
	f.fakeOCI.Results[ociRef] = &ociclient.PullResult{
		Data:   chartOCI,
		Digest: ociDigest,
		Size:   int64(len(chartOCI)),
		Meta:   ociclient.ChartMeta{Name: "redis", Version: "7.0.0"},
	}

	if err := f.runSync(t); err != nil {
		t.Fatalf("mixed-index sync: %v", err)
	}
	rows, _ := f.helmCharts.ListByRepo(context.Background(), f.repoID)
	if len(rows) != 2 {
		t.Fatalf("mixed-index helm_charts rows = %d; want 2 (http+oci)", len(rows))
	}
}

// TestOCISync_NilOCIClient_FailsGracefully — if the SyncDeps.OCIClient is
// nil but an oci:// entry appears, the sync returns a descriptive error
// rather than panicking. Exercises the defensive nil-guard in
// fetchAndCommitOCI. No helm_charts row inserted for the oci entry.
func TestOCISync_NilOCIClient_FailsGracefully(t *testing.T) {
	chartOCI := buildOCIChartTGZ(t, "redis", "7.0.0")
	ociRef := "registry-1.docker.io/bitnamicharts/redis:7.0.0"

	index := makeOCIIndex([]ociIndexEntry{
		{Name: "redis", Version: "7.0.0", URL: "oci://" + ociRef, Digest: shaHex(chartOCI)},
	})

	// Build a fixture like newOCIHelmFixture but manually omit OCIClient.
	db := sqlitetest.New(t)
	reposRepo := metadata.NewReposRepo(db)
	projectsRepo := metadata.NewProjectsRepo(db)
	helmCharts := metadata.NewHelmChartsRepo(db)
	scans := metadata.NewScansRepo(db)

	mux := http.NewServeMux()
	mux.HandleFunc("/index.yaml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-yaml")
		_, _ = w.Write([]byte(index))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	ctx := context.Background()
	pid, err := projectsRepo.Create(ctx, "pnil", "nil-ociclient test")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	rid, err := reposRepo.Create(ctx, pid, "helm", "rnil", "", nil, nil, nil)
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}

	dataRoot := t.TempDir()
	repoRoot := filepath.Join(dataRoot, "repos")
	pathStore := storage.NewPathStore(repoRoot)

	rec := &recordingAudit{}
	h := helm.NewSyncHandler(helm.SyncDeps{
		DB:         db,
		Path:       pathStore,
		HelmCharts: helmCharts,
		Repos:      reposRepo,
		Projects:   projectsRepo,
		Scans:      scans,
		Audit:      rec,
		Coalescer:  nil,
		HTTPClient: srv.Client(),
		RepoRoot:   repoRoot,
		Cfg:        config.SyncConfig{MaxParallelDownloadsPerJob: 1},
		SyncJobs:   metadata.NewSyncJobsRepo(db),
		// OCIClient intentionally NIL.
	})

	jobID := seedHelmSyncJobRow(t, db, rid)
	payload := map[string]string{"upstream_url": srv.URL}
	pb, _ := json.Marshal(payload)
	err = h.Handle(ctx, string(pb), 0, rid, jobID)
	if err == nil {
		t.Fatalf("Handle returned nil; want a descriptive error for nil OCIClient")
	}
	if !strings.Contains(err.Error(), "OCIClient") && !strings.Contains(err.Error(), "oci") {
		t.Errorf("error = %q; want it to mention OCIClient/oci", err)
	}
}

// TestRegenIndexServesHTTPURLs_OCISourced — after an OCI-sourced chart is
// committed, the regen path (helm.sh/helm/v3/pkg/repo.IndexDirectory) MUST
// emit urls pointing at our serving host ("charts/<name>-<version>.tgz"),
// NEVER the upstream oci:// URL. Proves OCIHELM-06: OCI-sourced charts are
// served over HTTP.
func TestRegenIndexServesHTTPURLs_OCISourced(t *testing.T) {
	chart := buildOCIChartTGZ(t, "nginx", "1.0.0")
	chartDigest := sha256OfBytes(chart)
	ociRef := "registry-1.docker.io/bitnamicharts/nginx:1.0.0"

	index := makeOCIIndex([]ociIndexEntry{
		{Name: "nginx", Version: "1.0.0", URL: "oci://" + ociRef},
	})

	f := newOCIHelmFixture(t, index, nil)
	f.fakeOCI.Results[ociRef] = &ociclient.PullResult{
		Data:   chart,
		Digest: chartDigest,
		Size:   int64(len(chart)),
		Meta:   ociclient.ChartMeta{Name: "nginx", Version: "1.0.0"},
	}
	if err := f.runSync(t); err != nil {
		t.Fatalf("sync: %v", err)
	}

	// Regenerate the index directly — the sync handler's Coalescer was
	// nil so we drive it manually.
	repoDir := filepath.Join(f.repoRoot, f.projName, "helm", f.repoName)
	chartsDir := filepath.Join(repoDir, "charts")
	idx, err := indexDirectory(t, chartsDir, "charts")
	if err != nil {
		t.Fatalf("IndexDirectory: %v", err)
	}
	if _, ok := idx.Entries["nginx"]; !ok {
		t.Fatalf("index entries missing nginx; got %v", idx.Entries)
	}
	for _, v := range idx.Entries["nginx"] {
		for _, u := range v.URLs {
			if strings.HasPrefix(u, "oci://") {
				t.Errorf("regenerated URL = %q; want HTTP-scheme (not oci://) per OCIHELM-06", u)
			}
		}
	}
}

// indexDirectory is a thin wrapper around helmrepo.IndexDirectory that the
// test uses rather than importing the helm repo package directly into the
// test imports list — keeps the header lean.
func indexDirectory(t *testing.T, chartsDir, baseURL string) (*helmrepo.IndexFile, error) {
	t.Helper()
	return helmrepo.IndexDirectory(chartsDir, baseURL)
}

