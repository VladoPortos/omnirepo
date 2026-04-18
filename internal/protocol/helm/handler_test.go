package helm_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/dxc-internal/omnirepo/internal/audit"
	"github.com/dxc-internal/omnirepo/internal/auth"
	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/metadata/sqlitetest"
	"github.com/dxc-internal/omnirepo/internal/protocol/helm"
	"github.com/dxc-internal/omnirepo/internal/protocol/regen"
	"github.com/dxc-internal/omnirepo/internal/storage"
)

// fixture wires a full Helm handler against tmp data root + sqlitetest DB and
// returns a running httptest.Server. Includes a kick-observing coalescer
// factory so tests can assert Kick() was called after upload/delete.
type fixture struct {
	t        *testing.T
	db       *metadata.DB
	users    *metadata.UsersRepo
	apiKeys  *metadata.APIKeysRepo
	repos    *metadata.ReposRepo
	projects *metadata.ProjectsRepo
	charts   *metadata.HelmChartsRepo
	scans    *metadata.ScansRepo
	auditLog audit.Logger
	srv      *httptest.Server
	dataRoot string
	repoRoot string
	login    string
	password string
	userID   int64

	kickCounts sync.Map // repoID -> *atomic.Int64
	registry   *regen.Registry
	path       storage.PathStore
}

// pathStore returns the fixture's shared PathStore rooted at repoRoot — the
// same store the Helm handler uses — so tests like oci_mirror_test.go can
// construct a helm.Mirror that writes into the same tree.
func (f *fixture) pathStore() storage.PathStore { return f.path }

func (f *fixture) kickCount(repoID int64) int64 {
	v, ok := f.kickCounts.Load(repoID)
	if !ok {
		return 0
	}
	return v.(*atomic.Int64).Load()
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	db := sqlitetest.New(t)
	users := metadata.NewUsersRepo(db)
	apiKeys := metadata.NewAPIKeysRepo(db)
	repos := metadata.NewReposRepo(db)
	projects := metadata.NewProjectsRepo(db)
	charts := metadata.NewHelmChartsRepo(db)
	scans := metadata.NewScansRepo(db)
	sessions := metadata.NewSessionsRepo(db)

	login := "helm-user"
	password := "helm-test-password-1234567"
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatalf("hash pw: %v", err)
	}
	uid, err := users.Create(context.Background(), login, "u@example.com", hash, false, false)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	dataRoot := t.TempDir()
	repoRoot := filepath.Join(dataRoot, "repos")
	trashRoot := filepath.Join(dataRoot, "trash")
	if err := os.MkdirAll(repoRoot, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(trashRoot, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	auditPath := filepath.Join(dataRoot, "logs", "audit.log")
	if err := os.MkdirAll(filepath.Dir(auditPath), 0o750); err != nil {
		t.Fatalf("mkdir log: %v", err)
	}
	auditLogger, err := audit.New(db, auditPath, 10, 1)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}

	f := &fixture{
		t:        t,
		db:       db,
		users:    users,
		apiKeys:  apiKeys,
		repos:    repos,
		projects: projects,
		charts:   charts,
		scans:    scans,
		auditLog: auditLogger,
		dataRoot: dataRoot,
		repoRoot: repoRoot,
		login:    login,
		password: password,
		userID:   uid,
	}

	// Observer factory: every RegenFn increments the per-repo kick counter.
	// Tests that want a real regen override this via f.registry replacement.
	factory := func(repoID int64) regen.RegenFn {
		ctr := &atomic.Int64{}
		f.kickCounts.Store(repoID, ctr)
		return func(ctx context.Context) error {
			ctr.Add(1)
			return nil
		}
	}
	// Tiny debounce so tests observe kicks quickly.
	f.registry = regen.NewRegistry(10*time.Millisecond, 100*time.Millisecond, factory)
	t.Cleanup(func() { _ = f.registry.ShutdownAll(context.Background()) })

	f.path = storage.NewPathStore(repoRoot)
	h := helm.New(helm.Deps{
		DB:          db,
		Users:       users,
		APIKeys:     apiKeys,
		Sessions:    sessions,
		Repos:       repos,
		Projects:    projects,
		Members:     metadata.NewMembersRepo(db),
		HelmCharts:  charts,
		Scans:       scans,
		Coalescer:   f.registry,
		Path:        f.path,
		Trash:       storage.NewTrash(trashRoot),
		Audit:       auditLogger,
		MaxPutBytes: 1 << 20,
		RepoRoot:    repoRoot,
	})

	r := chi.NewRouter()
	h.Mount(r)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	f.srv = srv
	return f
}

// seedRepo creates a project + helm repo with the given public_read setting
// and enrolls the fixture user as a member.
func (f *fixture) seedRepo(projName, repoName string, publicRead, autoScan bool) (projectID, repoID int64) {
	pid, err := f.projects.Create(context.Background(), projName, "test")
	if err != nil {
		f.t.Fatalf("seed project: %v", err)
	}
	if _, err := f.db.Writer.Exec(`INSERT INTO project_members(project_id, user_id) VALUES (?, ?)`, pid, f.userID); err != nil {
		f.t.Fatalf("seed member: %v", err)
	}
	rid, err := f.repos.Create(context.Background(), pid, "helm", repoName, "", &autoScan, nil, &publicRead)
	if err != nil {
		f.t.Fatalf("seed repo: %v", err)
	}
	return pid, rid
}

func (f *fixture) basicAuth() string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(f.login+":"+f.password))
}

func (f *fixture) put(t *testing.T, urlPath string, body []byte, withAuth bool) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPut, f.srv.URL+urlPath, bytes.NewReader(body))
	if withAuth {
		req.Header.Set("Authorization", f.basicAuth())
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT %s: %v", urlPath, err)
	}
	return resp
}

func (f *fixture) get(t *testing.T, urlPath string, withAuth bool) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, f.srv.URL+urlPath, nil)
	if withAuth {
		req.Header.Set("Authorization", f.basicAuth())
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", urlPath, err)
	}
	return resp
}

func (f *fixture) del(t *testing.T, urlPath string, withAuth bool) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodDelete, f.srv.URL+urlPath, nil)
	if withAuth {
		req.Header.Set("Authorization", f.basicAuth())
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE %s: %v", urlPath, err)
	}
	return resp
}

// waitForKick polls up to 1s for at least expected kicks on repoID.
func (f *fixture) waitForKick(t *testing.T, repoID int64, expected int64) {
	t.Helper()
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if f.kickCount(repoID) >= expected {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("kick count for repo %d: got %d, want >= %d", repoID, f.kickCount(repoID), expected)
}

// -----------------------------------------------------------------------------
// Tests
// -----------------------------------------------------------------------------

func TestHelmPutChartStoresRow(t *testing.T) {
	f := newFixture(t)
	_, rid := f.seedRepo("proj1", "charts1", false, false)

	tgz := makeChartTGZ(t, "mychart", "1.2.3", "v1", "a test chart", []string{"a", "b"})
	resp := f.put(t, "/proj1/helm/charts1/charts/mychart-1.2.3.tgz", tgz, true)
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("PUT status=%d body=%s", resp.StatusCode, b)
	}
	resp.Body.Close()

	// File on disk.
	diskPath := filepath.Join(f.repoRoot, "proj1", "helm", "charts1", "charts", "mychart-1.2.3.tgz")
	got, err := os.ReadFile(diskPath)
	if err != nil {
		t.Fatalf("read on-disk: %v", err)
	}
	if !bytes.Equal(got, tgz) {
		t.Fatalf("on-disk body mismatch")
	}

	// helm_charts row.
	row, err := f.charts.FindByNameVersion(context.Background(), rid, "mychart", "1.2.3")
	if err != nil || row == nil {
		t.Fatalf("helm_charts row missing: %v", err)
	}
	if row.Filename != "mychart-1.2.3.tgz" {
		t.Fatalf("filename: %q", row.Filename)
	}
	if row.SizeBytes != int64(len(tgz)) {
		t.Fatalf("size: %d", row.SizeBytes)
	}
	if !strings.HasPrefix(row.Digest, "sha256:") {
		t.Fatalf("digest: %q", row.Digest)
	}

	// helm_fts row present.
	var n int
	if err := f.db.Reader.QueryRow(
		`SELECT COUNT(*) FROM helm_fts WHERE repo_id=? AND name=? AND version=?`,
		rid, "mychart", "1.2.3",
	).Scan(&n); err != nil {
		t.Fatalf("helm_fts count: %v", err)
	}
	if n != 1 {
		t.Fatalf("helm_fts rows = %d, want 1", n)
	}

	// repos.metadata_state flipped to dirty.
	state, _, err := f.repos.GetMetadataState(context.Background(), rid)
	if err != nil {
		t.Fatalf("GetMetadataState: %v", err)
	}
	if state != metadata.MetadataStateDirty {
		t.Fatalf("metadata_state=%q", state)
	}

	// Coalescer kicked.
	f.waitForKick(t, rid, 1)
}

func TestHelmPutProvPassThrough(t *testing.T) {
	f := newFixture(t)
	_, rid := f.seedRepo("proj1", "charts1", false, false)

	// Upload chart first.
	tgz := makeChartTGZ(t, "mychart", "1.0.0", "", "", nil)
	resp := f.put(t, "/proj1/helm/charts1/charts/mychart-1.0.0.tgz", tgz, true)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT chart status=%d", resp.StatusCode)
	}
	resp.Body.Close()
	f.waitForKick(t, rid, 1)
	kicksBefore := f.kickCount(rid)

	// Now upload provenance.
	prov := []byte("-----BEGIN PGP SIGNATURE-----\n(fake sig)\n-----END PGP SIGNATURE-----\n")
	resp = f.put(t, "/proj1/helm/charts1/charts/mychart-1.0.0.tgz.prov", prov, true)
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("PUT prov status=%d body=%s", resp.StatusCode, b)
	}
	resp.Body.Close()

	// Prov file on disk.
	provPath := filepath.Join(f.repoRoot, "proj1", "helm", "charts1", "charts", "mychart-1.0.0.tgz.prov")
	if got, err := os.ReadFile(provPath); err != nil {
		t.Fatalf("read prov: %v", err)
	} else if !bytes.Equal(got, prov) {
		t.Fatalf("prov body mismatch")
	}

	// No helm_charts row for a .prov filename (should be 1 for the chart only).
	var n int
	if err := f.db.Reader.QueryRow(`SELECT COUNT(*) FROM helm_charts WHERE repo_id=?`, rid).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("helm_charts rows = %d, want 1 (only chart)", n)
	}

	// Prov upload should NOT have kicked the coalescer.
	time.Sleep(50 * time.Millisecond) // give any stray kick time to land
	if got := f.kickCount(rid); got != kicksBefore {
		t.Fatalf("prov upload kicked coalescer: before=%d after=%d", kicksBefore, got)
	}
}

func TestHelmParseRejectsInvalidTgz(t *testing.T) {
	f := newFixture(t)
	f.seedRepo("proj1", "charts1", false, false)

	body := []byte("this is not a valid helm chart")
	resp := f.put(t, "/proj1/helm/charts1/charts/bogus-1.0.0.tgz", body, true)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("PUT status=%d want 400", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(b), "invalid_package") {
		t.Fatalf("expected invalid_package in body: %q", b)
	}
}

func TestHelmGetChart(t *testing.T) {
	f := newFixture(t)
	f.seedRepo("proj1", "charts1", false, false)

	tgz := makeChartTGZ(t, "mychart", "1.2.3", "", "", nil)
	resp := f.put(t, "/proj1/helm/charts1/charts/mychart-1.2.3.tgz", tgz, true)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT: %d", resp.StatusCode)
	}
	resp.Body.Close()

	resp = f.get(t, "/proj1/helm/charts1/charts/mychart-1.2.3.tgz", true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET: %d", resp.StatusCode)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !bytes.Equal(got, tgz) {
		t.Fatalf("GET body mismatch")
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/gzip" {
		t.Fatalf("Content-Type: %q", ct)
	}
}

func TestHelmGetIndexYAMLEmptyRepo(t *testing.T) {
	f := newFixture(t)
	f.seedRepo("proj1", "charts1", true, false) // public_read

	// Anonymous should succeed on a public_read repo.
	resp := f.get(t, "/proj1/helm/charts1/index.yaml", false)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET status=%d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "apiVersion") {
		t.Fatalf("index body: %q", body)
	}
}

func TestHelmGetIndexYAMLForbiddenForPrivate(t *testing.T) {
	f := newFixture(t)
	f.seedRepo("proj1", "charts1", false, false) // NOT public_read

	// Anonymous should be rejected.
	resp := f.get(t, "/proj1/helm/charts1/index.yaml", false)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anon GET status=%d want 401", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestHelmDeleteChart(t *testing.T) {
	f := newFixture(t)
	_, rid := f.seedRepo("proj1", "charts1", false, false)

	tgz := makeChartTGZ(t, "mychart", "1.0.0", "", "", nil)
	resp := f.put(t, "/proj1/helm/charts1/charts/mychart-1.0.0.tgz", tgz, true)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT: %d", resp.StatusCode)
	}
	resp.Body.Close()
	f.waitForKick(t, rid, 1)

	resp = f.del(t, "/proj1/helm/charts1/charts/mychart-1.0.0.tgz", true)
	if resp.StatusCode != http.StatusNoContent {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("DELETE status=%d body=%s", resp.StatusCode, b)
	}
	resp.Body.Close()

	// Row gone.
	row, _ := f.charts.FindByFilename(context.Background(), rid, "mychart-1.0.0.tgz")
	if row != nil {
		t.Fatalf("row still present after delete")
	}
	// FTS row gone.
	var n int
	_ = f.db.Reader.QueryRow(`SELECT COUNT(*) FROM helm_fts WHERE repo_id=?`, rid).Scan(&n)
	if n != 0 {
		t.Fatalf("helm_fts rows after delete = %d, want 0", n)
	}
	// Coalescer kicked a second time.
	f.waitForKick(t, rid, 2)
}

func TestHelmUploadForbiddenForOutsider(t *testing.T) {
	f := newFixture(t)
	// Seed repo with the fixture user as member.
	f.seedRepo("proj1", "charts1", false, false)

	// Create a second project the user is NOT a member of.
	pidOther, err := f.projects.Create(context.Background(), "proj2", "")
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	autoScan := false
	publicRead := false
	if _, err := f.repos.Create(context.Background(), pidOther, "helm", "charts1", "", &autoScan, nil, &publicRead); err != nil {
		t.Fatalf("repo: %v", err)
	}

	tgz := makeChartTGZ(t, "mychart", "1.0.0", "", "", nil)
	resp := f.put(t, "/proj2/helm/charts1/charts/mychart-1.0.0.tgz", tgz, true)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status=%d want 403", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestHelmInvalidFilenameRejected(t *testing.T) {
	f := newFixture(t)
	f.seedRepo("proj1", "charts1", false, false)

	// Malformed: neither .tgz nor .prov.
	resp := f.put(t, "/proj1/helm/charts1/charts/README.txt", []byte("x"), true)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", resp.StatusCode)
	}
	resp.Body.Close()
}
