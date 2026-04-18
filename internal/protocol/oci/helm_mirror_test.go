package oci_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/dxc-internal/omnirepo/internal/audit"
	"github.com/dxc-internal/omnirepo/internal/auth"
	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/metadata/sqlitetest"
	"github.com/dxc-internal/omnirepo/internal/protocol/helm"
	"github.com/dxc-internal/omnirepo/internal/protocol/oci"
	"github.com/dxc-internal/omnirepo/internal/protocol/regen"
	"github.com/dxc-internal/omnirepo/internal/storage"
)

// helmMirrorFixture wires a /v2 handler with a real helm.Mirror adapter so
// we can exercise the end-to-end OCI push → helm mirror flow.
type helmMirrorFixture struct {
	t        *testing.T
	db       *metadata.DB
	members  *metadata.MembersRepo
	repos    *metadata.ReposRepo
	projects *metadata.ProjectsRepo
	charts   *metadata.HelmChartsRepo
	blobs    *metadata.DockerBlobsRepo
	cas      storage.CAS
	srv      *httptest.Server
	dataRoot string
	repoRoot string
	login    string
	password string
	token    string
	// Observed helm regen kicks. The helm.Mirror calls coalescer.Get(repoID).Kick();
	// the per-repoID counter lives in kickCounts.
	kickCounts sync.Map // repoID -> *int64
	registry   *regen.Registry
}

// buildHelmChartTGZ builds an in-memory Helm chart archive matching what the
// helm SDK loader expects. Mirrors the helm test fixture
// (internal/protocol/helm/testutil_test.go).
func buildHelmChartTGZ(t *testing.T, name, version string) []byte {
	t.Helper()
	chartYAML := fmt.Sprintf(`apiVersion: v2
name: %s
version: %s
appVersion: "1.0"
description: mirror test chart
type: application
`, name, version)
	notes := "Test chart NOTES\n"
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	writeTarFile := func(path, body string) {
		h := &tar.Header{
			Name:     path,
			Mode:     0o644,
			Size:     int64(len(body)),
			Typeflag: tar.TypeReg,
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatalf("tar header %s: %v", path, err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatalf("tar body %s: %v", path, err)
		}
	}
	writeTarFile(name+"/Chart.yaml", chartYAML)
	writeTarFile(name+"/templates/NOTES.txt", notes)
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gz close: %v", err)
	}
	return buf.Bytes()
}

// seedCASBlob writes content to the CAS and upserts a docker_blobs row so
// manifestPut's referenced-digest validation passes. Returns the sha256
// digest.
func (f *helmMirrorFixture) seedCASBlob(content []byte) string {
	digest, _, err := f.cas.Put(context.Background(), bytes.NewReader(content))
	if err != nil {
		f.t.Fatalf("cas put: %v", err)
	}
	// Seed the docker_blobs row at ref_count=0 via a direct exec (avoids
	// tying the helper to the signature of DockerBlobsRepo.UpsertZeroRef,
	// which takes a *sql.Tx). The manifestPut path reads from this table
	// to validate referenced blobs.
	if _, err := f.db.Writer.ExecContext(context.Background(),
		`INSERT OR IGNORE INTO docker_blobs(digest, size_bytes, ref_count, last_touched_at)
		 VALUES (?, ?, 0, CURRENT_TIMESTAMP)`,
		digest, int64(len(content)),
	); err != nil {
		f.t.Fatalf("blobs upsert: %v", err)
	}
	return digest
}

// newHelmMirrorFixture builds a full /v2 handler with the helm.Mirror
// adapter wired in, plus a helm_charts / helm-type repo so the mirror path
// is exercised end-to-end.
func newHelmMirrorFixture(t *testing.T) *helmMirrorFixture {
	t.Helper()
	db := sqlitetest.New(t)
	dataRoot := t.TempDir()
	repoRoot := filepath.Join(dataRoot, "repos")
	_ = os.MkdirAll(filepath.Join(dataRoot, "tmp", "uploads"), 0o750)
	_ = os.MkdirAll(filepath.Join(dataRoot, "blobs"), 0o750)
	_ = os.MkdirAll(repoRoot, 0o750)

	users := metadata.NewUsersRepo(db)
	apiKeys := metadata.NewAPIKeysRepo(db)
	reposRepo := metadata.NewReposRepo(db)
	projectsRepo := metadata.NewProjectsRepo(db)
	sessions := metadata.NewSessionsRepo(db)
	members := metadata.NewMembersRepo(db)
	blobsRepo := metadata.NewDockerBlobsRepo(db)
	manifestsRepo := metadata.NewDockerManifestsRepo(db)
	tagsRepo := metadata.NewDockerTagsRepo(db)
	scansRepo := metadata.NewScansRepo(db)
	helmCharts := metadata.NewHelmChartsRepo(db)
	cas := storage.NewCAS(filepath.Join(dataRoot, "blobs"))
	pathStore := storage.NewPathStore(repoRoot)

	realAudit, err := audit.New(db, filepath.Join(dataRoot, "audit.log"), 10, 2)
	if err != nil {
		t.Fatal(err)
	}

	login := "mirror-user"
	password := "correct-horse-battery-staple-42"
	pwHash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	uid, err := users.Create(context.Background(), login, "u@example.com", pwHash, false, false)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := projectsRepo.Create(context.Background(), "proj", "helm mirror test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reposRepo.Create(context.Background(), pid, "helm", "mirror", "", nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := members.Add(context.Background(), pid, uid); err != nil {
		t.Fatal(err)
	}

	f := &helmMirrorFixture{
		t:        t,
		db:       db,
		members:  members,
		repos:    reposRepo,
		projects: projectsRepo,
		charts:   helmCharts,
		blobs:    blobsRepo,
		cas:      cas,
		dataRoot: dataRoot,
		repoRoot: repoRoot,
		login:    login,
		password: password,
	}

	// Kick-observing regen factory: each Kick increments a per-repoID
	// counter, identical to the helm fixture in handler_test.go. Lets us
	// assert the mirror's coalescer.Kick ran.
	factory := func(repoID int64) regen.RegenFn {
		ctr := new(int64)
		f.kickCounts.Store(repoID, ctr)
		return func(ctx context.Context) error {
			*ctr++
			return nil
		}
	}
	f.registry = regen.NewRegistry(10*time.Millisecond, 100*time.Millisecond, factory)
	t.Cleanup(func() { _ = f.registry.ShutdownAll(context.Background()) })

	mirror := helm.NewMirror(db, helmCharts, reposRepo, pathStore, f.registry)
	hook := &testHelmMirrorAdapter{cas: cas, mirror: mirror}

	secret := []byte("0123456789abcdef0123456789abcdef")
	handler := oci.New(oci.Deps{
		DB:          db,
		Users:       users,
		APIKeys:     apiKeys,
		Repos:       reposRepo,
		Projects:    projectsRepo,
		Sessions:    sessions,
		Members:     members,
		CAS:         cas,
		Blobs:       blobsRepo,
		BlobUploads: metadata.NewBlobUploadsRepo(db),
		Sess:        metadata.NewBlobUploadSessionsRepo(db),
		Audit:       realAudit,
		DataRoot:    dataRoot,
		HMACSecret:  secret,
		JWTTTL:      time.Hour,
		Manifests:   manifestsRepo,
		Tags:        tagsRepo,
		Scans:       scansRepo,
		HelmMirror:  hook,
	})
	r := chi.NewRouter()
	handler.Mount(r)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	f.srv = srv
	f.token = f.mintToken()
	return f
}

// testHelmMirrorAdapter is the in-package analog of the production
// ociHelmMirrorAdapter in internal/app. We re-declare it here instead of
// importing internal/app to avoid an import cycle.
type testHelmMirrorAdapter struct {
	cas    storage.CAS
	mirror *helm.Mirror
}

func (a *testHelmMirrorAdapter) Mirror(ctx context.Context, projectName, repoName, digest string) error {
	rc, err := a.cas.Get(ctx, digest)
	if err != nil {
		return fmt.Errorf("cas get: %w", err)
	}
	defer func() { _ = rc.Close() }()
	return a.mirror.MirrorToTraditional(ctx, projectName, repoName, rc)
}

func (f *helmMirrorFixture) mintToken() string {
	req, _ := http.NewRequest("GET", f.srv.URL+"/v2/token", nil)
	req.Header.Set("Authorization", "Basic "+basicEncode(f.login+":"+f.password))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatalf("mint token: %v", err)
	}
	defer resp.Body.Close()
	var p struct {
		Token string `json:"token"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&p)
	if p.Token == "" {
		f.t.Fatalf("empty token")
	}
	return p.Token
}

// putHelmManifest uploads a manifest to /v2/proj/helm/mirror/manifests/<ref>
// and returns the response.
func (f *helmMirrorFixture) putHelmManifest(ref string, body []byte) *http.Response {
	url := fmt.Sprintf("%s/v2/proj/helm/mirror/manifests/%s", f.srv.URL, ref)
	req, _ := http.NewRequest("PUT", url, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+f.token)
	req.Header.Set("Content-Type", oci.MediaTypeOCIManifest)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatalf("put manifest: %v", err)
	}
	return resp
}

// waitForKick polls up to 1s for at least expected kicks on repoID.
func (f *helmMirrorFixture) waitForKick(t *testing.T, repoID int64, expected int64) {
	t.Helper()
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if v, ok := f.kickCounts.Load(repoID); ok {
			if *v.(*int64) >= expected {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	var got int64
	if v, ok := f.kickCounts.Load(repoID); ok {
		got = *v.(*int64)
	}
	t.Fatalf("kick count for repo %d: got %d, want >= %d", repoID, got, expected)
}

// TestOCIManifestPut_MirrorsHelmToTraditional is the plan 07-04 Task 2
// positive-path gate. A helm OCI push (config mediaType = HelmChartConfigV1
// + single layer mediaType = HelmChartContentV1) must produce the
// traditional chart file and a helm_charts row.
func TestOCIManifestPut_MirrorsHelmToTraditional(t *testing.T) {
	f := newHelmMirrorFixture(t)

	tgz := buildHelmChartTGZ(t, "foo", "0.1.0")
	chartDigest := f.seedCASBlob(tgz)

	// Empty-config blob satisfies the manifest's config.digest validation.
	configBytes := []byte("{}")
	configDigest := f.seedCASBlob(configBytes)

	manifest := map[string]any{
		"schemaVersion": 2,
		"config": map[string]any{
			"mediaType": oci.MediaTypeHelmChartConfigV1,
			"digest":    configDigest,
			"size":      len(configBytes),
		},
		"layers": []map[string]any{{
			"mediaType": oci.MediaTypeHelmChartContentV1,
			"digest":    chartDigest,
			"size":      len(tgz),
		}},
	}
	body, _ := json.Marshal(manifest)

	resp := f.putHelmManifest("0.1.0", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("manifest put: status=%d body=%s", resp.StatusCode, b)
	}

	// Assert mirror file exists at traditional location.
	wantPath := filepath.Join(f.repoRoot, "proj", "helm", "mirror", "charts", "foo-0.1.0.tgz")
	gotTgz, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatalf("mirror file not at %s: %v", wantPath, err)
	}
	if !bytes.Equal(gotTgz, tgz) {
		t.Errorf("mirror body mismatch: len=%d want=%d", len(gotTgz), len(tgz))
	}

	// Assert helm_charts row landed.
	var n int
	if err := f.db.Reader.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM helm_charts WHERE name='foo' AND version='0.1.0'`,
	).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("helm_charts count = %d; want 1", n)
	}

	// Assert the regen coalescer got kicked on the helm repo's id.
	helmRepo, err := f.repos.FindByTriple(context.Background(), 1, "helm", "mirror")
	if err != nil || helmRepo == nil {
		t.Fatalf("resolve helm repo: %v", err)
	}
	f.waitForKick(t, helmRepo.ID, 1)
}

// TestOCIManifestPut_MirrorsHelmWithProvenanceLayer validates the
// mediaType-based layer selection: given BOTH a chart-content layer AND a
// provenance layer (with provenance listed FIRST so order cannot be
// relied on), the mirror must pick the chart layer by mediaType and write
// the chart bytes — NOT the provenance bytes.
func TestOCIManifestPut_MirrorsHelmWithProvenanceLayer(t *testing.T) {
	f := newHelmMirrorFixture(t)

	tgz := buildHelmChartTGZ(t, "foo", "0.2.0")
	chartDigest := f.seedCASBlob(tgz)
	prov := []byte("-----BEGIN PGP SIGNED MESSAGE-----\nHash: SHA256\n\nfake prov\n-----END PGP SIGNATURE-----\n")
	provDigest := f.seedCASBlob(prov)
	configBytes := []byte("{}")
	configDigest := f.seedCASBlob(configBytes)

	manifest := map[string]any{
		"schemaVersion": 2,
		"config": map[string]any{
			"mediaType": oci.MediaTypeHelmChartConfigV1,
			"digest":    configDigest,
			"size":      len(configBytes),
		},
		// Provenance first to prove mediaType drives selection, not index.
		"layers": []map[string]any{
			{
				"mediaType": "application/vnd.cncf.helm.chart.provenance.v1.prov",
				"digest":    provDigest,
				"size":      len(prov),
			},
			{
				"mediaType": oci.MediaTypeHelmChartContentV1,
				"digest":    chartDigest,
				"size":      len(tgz),
			},
		},
	}
	body, _ := json.Marshal(manifest)

	resp := f.putHelmManifest("0.2.0", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("manifest put: status=%d body=%s", resp.StatusCode, b)
	}

	wantPath := filepath.Join(f.repoRoot, "proj", "helm", "mirror", "charts", "foo-0.2.0.tgz")
	gotTgz, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatalf("mirror missing: %v", err)
	}
	if !bytes.Equal(gotTgz, tgz) {
		t.Errorf("mirrored wrong layer (likely provenance, not chart)")
	}

	// helm_charts row for foo 0.2.0.
	var n int
	_ = f.db.Reader.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM helm_charts WHERE name='foo' AND version='0.2.0'`,
	).Scan(&n)
	if n != 1 {
		t.Errorf("helm_charts row count=%d want 1", n)
	}
}

// TestOCIManifestPut_HelmConfigWithNoChartLayer_SkipsMirror covers the
// forward-compat edge case: the manifest carries a Helm config mediaType
// but NO layer matches the chart-content mediaType. The mirror must SKIP
// (debug-log, not warn) and the OCI push must still return 201.
func TestOCIManifestPut_HelmConfigWithNoChartLayer_SkipsMirror(t *testing.T) {
	f := newHelmMirrorFixture(t)

	junk := []byte("not a chart")
	junkDigest := f.seedCASBlob(junk)
	configBytes := []byte("{}")
	configDigest := f.seedCASBlob(configBytes)

	manifest := map[string]any{
		"schemaVersion": 2,
		"config": map[string]any{
			"mediaType": oci.MediaTypeHelmChartConfigV1,
			"digest":    configDigest,
			"size":      len(configBytes),
		},
		"layers": []map[string]any{
			{
				"mediaType": "application/vnd.cncf.helm.chart.provenance.v1.prov",
				"digest":    junkDigest,
				"size":      len(junk),
			},
		},
	}
	body, _ := json.Marshal(manifest)

	resp := f.putHelmManifest("nochart", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("manifest put status=%d body=%s (should still be 201)", resp.StatusCode, b)
	}

	chartsDir := filepath.Join(f.repoRoot, "proj", "helm", "mirror", "charts")
	entries, err := os.ReadDir(chartsDir)
	if err == nil && len(entries) > 0 {
		t.Errorf("mirror fired despite no chart layer; wrote %d file(s)", len(entries))
	}
	var n int
	_ = f.db.Reader.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM helm_charts`,
	).Scan(&n)
	if n != 0 {
		t.Errorf("helm_charts count=%d want 0", n)
	}
}

// TestOCIManifestPut_NonHelm_DoesNotTriggerMirror asserts that a regular
// Docker manifest push (config.mediaType = docker image config) does NOT
// produce a file under /repos/<proj>/helm/<repo>/charts/. This is the
// false-positive guard (T-07-04-04).
func TestOCIManifestPut_NonHelm_DoesNotTriggerMirror(t *testing.T) {
	f := newHelmMirrorFixture(t)

	// Create a docker-type repo + a Docker push to it.
	dockerPID := int64(1) // same project, plain add
	if _, err := f.repos.Create(context.Background(), dockerPID, "docker", "app", "", nil, nil, nil); err != nil {
		t.Fatal(err)
	}

	cfgBlob := []byte(`{"architecture":"amd64","os":"linux"}`)
	cfgDigest := f.seedCASBlob(cfgBlob)
	layerBlob := []byte("layer bytes")
	layerDigest := f.seedCASBlob(layerBlob)

	manifest := map[string]any{
		"schemaVersion": 2,
		"mediaType":     oci.MediaTypeOCIManifest,
		"config": map[string]any{
			"mediaType": "application/vnd.oci.image.config.v1+json",
			"digest":    cfgDigest,
			"size":      len(cfgBlob),
		},
		"layers": []map[string]any{{
			"mediaType": "application/vnd.oci.image.layer.v1.tar+gzip",
			"digest":    layerDigest,
			"size":      len(layerBlob),
		}},
	}
	body, _ := json.Marshal(manifest)

	url := fmt.Sprintf("%s/v2/proj/docker/app/manifests/latest", f.srv.URL)
	req, _ := http.NewRequest("PUT", url, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+f.token)
	req.Header.Set("Content-Type", oci.MediaTypeOCIManifest)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("docker put: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("docker manifest put: status=%d body=%s", resp.StatusCode, b)
	}

	// Nothing should land under the helm charts tree.
	chartsDir := filepath.Join(f.repoRoot, "proj", "helm", "mirror", "charts")
	if entries, err := os.ReadDir(chartsDir); err == nil && len(entries) > 0 {
		t.Errorf("docker push wrote to helm mirror tree; %d entries", len(entries))
	}
	// And no helm_charts rows.
	var n int
	_ = f.db.Reader.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM helm_charts`,
	).Scan(&n)
	if n != 0 {
		t.Errorf("docker push created helm_charts rows: count=%d", n)
	}
}

// Unused stub to keep sha256 + hex imports when the fixture evolves.
var _ = func() (string, int) {
	h := sha256.Sum256(nil)
	return hex.EncodeToString(h[:]), 0
}
