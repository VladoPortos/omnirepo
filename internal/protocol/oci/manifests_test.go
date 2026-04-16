package oci_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/dxc-internal/omnirepo/internal/audit"
	"github.com/dxc-internal/omnirepo/internal/auth"
	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/metadata/sqlitetest"
	"github.com/dxc-internal/omnirepo/internal/protocol/oci"
	"github.com/dxc-internal/omnirepo/internal/storage"
)

// manifestFixture wires a /v2 handler with manifests + tags + scans so the
// full manifest upload path is exercised.
type manifestFixture struct {
	t         *testing.T
	db        *metadata.DB
	members   *metadata.MembersRepo
	manifests *metadata.DockerManifestsRepo
	tags      *metadata.DockerTagsRepo
	blobs     *metadata.DockerBlobsRepo
	scans     *metadata.ScansRepo
	repos     *metadata.ReposRepo
	projects  *metadata.ProjectsRepo
	cas       storage.CAS
	srv       *httptest.Server
	dataRoot  string
	projectID int64
	repoID    int64
	repoPath  string
	login     string
	password  string
	token     string
	audit     *manifestAudit
	kickCount int
}

type manifestAudit struct {
	mu   sync.Mutex
	real audit.Logger
	evts []audit.Event
}

func (m *manifestAudit) Record(ctx context.Context, e audit.Event) error {
	m.mu.Lock()
	m.evts = append(m.evts, e)
	m.mu.Unlock()
	if m.real != nil {
		return m.real.Record(ctx, e)
	}
	return nil
}

func (m *manifestAudit) kinds() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.evts))
	for _, e := range m.evts {
		out = append(out, string(e.Kind))
	}
	return out
}

func newManifestFixture(t *testing.T, autoScan bool) *manifestFixture {
	t.Helper()
	db := sqlitetest.New(t)
	dataRoot := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dataRoot, "tmp", "uploads"), 0o750)
	_ = os.MkdirAll(filepath.Join(dataRoot, "blobs"), 0o750)

	users := metadata.NewUsersRepo(db)
	apiKeys := metadata.NewAPIKeysRepo(db)
	repos := metadata.NewReposRepo(db)
	projects := metadata.NewProjectsRepo(db)
	sessions := metadata.NewSessionsRepo(db)
	members := metadata.NewMembersRepo(db)
	blobsRepo := metadata.NewDockerBlobsRepo(db)
	manifestsRepo := metadata.NewDockerManifestsRepo(db)
	tagsRepo := metadata.NewDockerTagsRepo(db)
	scansRepo := metadata.NewScansRepo(db)
	cas := storage.NewCAS(filepath.Join(dataRoot, "blobs"))

	ndjson := filepath.Join(dataRoot, "audit.log")
	realAudit, err := audit.New(db, ndjson, 10, 2)
	if err != nil {
		t.Fatal(err)
	}
	rec := &manifestAudit{real: realAudit}

	login := "pusher"
	password := "correct-horse-battery-staple-42"
	pwHash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	uid, err := users.Create(context.Background(), login, "u@example.com", pwHash, false, false)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := projects.Create(context.Background(), "proj", "test")
	if err != nil {
		t.Fatal(err)
	}
	var autoScanPtr *bool
	if autoScan {
		v := true
		autoScanPtr = &v
	}
	rid, err := repos.Create(context.Background(), pid, "docker", "app", "", autoScanPtr, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := members.Add(context.Background(), pid, uid); err != nil {
		t.Fatal(err)
	}

	secret := []byte("0123456789abcdef0123456789abcdef")
	f := &manifestFixture{
		t:         t,
		db:        db,
		members:   members,
		manifests: manifestsRepo,
		tags:      tagsRepo,
		blobs:     blobsRepo,
		scans:     scansRepo,
		repos:     repos,
		projects:  projects,
		cas:       cas,
		dataRoot:  dataRoot,
		projectID: pid,
		repoID:    rid,
		repoPath:  "proj/docker/app",
		login:     login,
		password:  password,
		audit:     rec,
	}
	handler := oci.New(oci.Deps{
		DB:          db,
		Users:       users,
		APIKeys:     apiKeys,
		Repos:       repos,
		Projects:    projects,
		Sessions:    sessions,
		Members:     members,
		CAS:         cas,
		Blobs:       blobsRepo,
		BlobUploads: metadata.NewBlobUploadsRepo(db),
		Sess:        metadata.NewBlobUploadSessionsRepo(db),
		Audit:       rec,
		DataRoot:    dataRoot,
		HMACSecret:  secret,
		JWTTTL:      time.Hour,
		Manifests:   manifestsRepo,
		Tags:        tagsRepo,
		Scans:       scansRepo,
		ScanKick: func() {
			f.kickCount++
		},
	})
	r := chi.NewRouter()
	handler.Mount(r)
	handler.MountCosign(r)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	f.srv = srv
	f.token = f.mintToken()
	return f
}

func (f *manifestFixture) mintToken() string {
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

// seedBlob inserts a fake blob row at ref_count=0 with the given digest/size.
// Tests use this to pre-seed referenced blobs so manifest PUT validation
// passes.
func (f *manifestFixture) seedBlob(content []byte) string {
	sum := sha256.Sum256(content)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	err := f.db.WriteTx(context.Background(), func(tx *sql.Tx) error {
		return f.blobs.UpsertZeroRef(context.Background(), tx, digest, int64(len(content)))
	})
	if err != nil {
		f.t.Fatalf("seed blob: %v", err)
	}
	return digest
}

// buildManifest returns a minimal OCI v1 image manifest referencing config
// and layers.
func buildManifest(configDigest string, layerDigests ...string) []byte {
	layers := make([]map[string]any, len(layerDigests))
	for i, d := range layerDigests {
		layers[i] = map[string]any{
			"mediaType": "application/vnd.oci.image.layer.v1.tar+gzip",
			"digest":    d,
			"size":      100,
		}
	}
	m := map[string]any{
		"schemaVersion": 2,
		"mediaType":     oci.MediaTypeOCIManifest,
		"config": map[string]any{
			"mediaType": "application/vnd.oci.image.config.v1+json",
			"digest":    configDigest,
			"size":      50,
		},
		"layers": layers,
	}
	b, _ := json.Marshal(m)
	return b
}

// putManifest uploads body at /v2/<name>/manifests/<ref>. Returns the response.
func (f *manifestFixture) putManifest(ref string, body []byte) *http.Response {
	url := fmt.Sprintf("%s/v2/%s/manifests/%s", f.srv.URL, f.repoPath, ref)
	req, _ := http.NewRequest("PUT", url, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+f.token)
	req.Header.Set("Content-Type", oci.MediaTypeOCIManifest)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatalf("put manifest: %v", err)
	}
	return resp
}

func (f *manifestFixture) getManifest(ref string) *http.Response {
	url := fmt.Sprintf("%s/v2/%s/manifests/%s", f.srv.URL, f.repoPath, ref)
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Authorization", "Bearer "+f.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatalf("get manifest: %v", err)
	}
	return resp
}

func (f *manifestFixture) deleteManifest(ref string) *http.Response {
	url := fmt.Sprintf("%s/v2/%s/manifests/%s", f.srv.URL, f.repoPath, ref)
	req, _ := http.NewRequest("DELETE", url, nil)
	req.Header.Set("Authorization", "Bearer "+f.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatalf("delete manifest: %v", err)
	}
	return resp
}

// TestManifestPutAndGetByteIdentical: body written must match bytes retrieved
// (T-02-07-01 / Pitfall 5).
func TestManifestPutAndGetByteIdentical(t *testing.T) {
	f := newManifestFixture(t, false)
	configDig := f.seedBlob([]byte("config-v1"))
	layerDig := f.seedBlob([]byte("layer-1-data"))
	body := buildManifest(configDig, layerDig)

	resp := f.putManifest("v1", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("put: %d body=%s", resp.StatusCode, b)
	}

	g := f.getManifest("v1")
	defer g.Body.Close()
	if g.StatusCode != http.StatusOK {
		t.Fatalf("get: %d", g.StatusCode)
	}
	got, _ := io.ReadAll(g.Body)
	if !bytes.Equal(got, body) {
		t.Fatalf("body mismatch: got %q want %q", got, body)
	}
	// Docker-Content-Digest header must be present.
	if g.Header.Get("Docker-Content-Digest") == "" {
		t.Fatal("missing Docker-Content-Digest header")
	}
}

// TestManifestOversizedReturns413 exceeds the 4 MiB cap → 413.
func TestManifestOversizedReturns413(t *testing.T) {
	f := newManifestFixture(t, false)
	// Fabricate a huge body (5 MiB of JSON whitespace-padded).
	big := bytes.Repeat([]byte("x"), int(oci.ManifestMaxBytes+1))
	resp := f.putManifest("big", big)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized: status %d; want 413", resp.StatusCode)
	}
}

// TestManifestMissingBlobReturns404 : referenced blob digest must exist.
func TestManifestMissingBlobReturns404(t *testing.T) {
	f := newManifestFixture(t, false)
	// Fake digest that doesn't exist.
	missing := "sha256:" + strings.Repeat("0", 64)
	body := buildManifest(missing)
	resp := f.putManifest("v1", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("missing blob: status %d; want 404; body=%s", resp.StatusCode, b)
	}
}

// TestRefDeltaOnTagOverwrite is the critical gate (Pitfall 1).
func TestRefDeltaOnTagOverwrite(t *testing.T) {
	f := newManifestFixture(t, false)
	cfg := f.seedBlob([]byte("cfg"))
	l1 := f.seedBlob([]byte("layer-1"))
	l2 := f.seedBlob([]byte("layer-2"))
	l3 := f.seedBlob([]byte("layer-3"))

	// Manifest A = [cfg, l1, l2].
	bodyA := buildManifest(cfg, l1, l2)
	respA := f.putManifest("v1", bodyA)
	respA.Body.Close()
	if respA.StatusCode != http.StatusCreated {
		t.Fatalf("push A: %d", respA.StatusCode)
	}

	// Manifest B = [cfg, l1, l3] (l2 gone, l3 new).
	bodyB := buildManifest(cfg, l1, l3)
	respB := f.putManifest("v1", bodyB)
	respB.Body.Close()
	if respB.StatusCode != http.StatusCreated {
		t.Fatalf("push B: %d", respB.StatusCode)
	}

	// Assertions:
	//   cfg: ref_count == 1 (still referenced by B)
	//   l1 : ref_count == 1 (still referenced by B)
	//   l2 : ref_count == 0 (decremented)
	//   l3 : ref_count == 1 (newly referenced)
	cases := map[string]int64{cfg: 1, l1: 1, l2: 0, l3: 1}
	for digest, want := range cases {
		b, err := f.blobs.Stat(context.Background(), digest)
		if err != nil {
			t.Fatalf("stat %s: %v", digest, err)
		}
		if b == nil {
			t.Fatalf("blob %s missing", digest)
		}
		if b.RefCount != want {
			t.Fatalf("%s ref_count=%d want=%d", digest, b.RefCount, want)
		}
	}
}

// TestManifestDeleteByDigestDecrementsRefs.
func TestManifestDeleteByDigestDecrementsRefs(t *testing.T) {
	f := newManifestFixture(t, false)
	cfg := f.seedBlob([]byte("cfg"))
	l1 := f.seedBlob([]byte("layer-1"))
	body := buildManifest(cfg, l1)
	resp := f.putManifest("v1", body)
	resp.Body.Close()
	mfDigest := resp.Header.Get("Docker-Content-Digest")
	if mfDigest == "" {
		t.Fatal("no digest in put response")
	}

	del := f.deleteManifest(mfDigest)
	del.Body.Close()
	if del.StatusCode != http.StatusAccepted {
		t.Fatalf("delete: %d", del.StatusCode)
	}

	// Both blobs should be ref_count=0 now.
	for _, d := range []string{cfg, l1} {
		b, err := f.blobs.Stat(context.Background(), d)
		if err != nil || b == nil {
			t.Fatalf("stat %s: %v", d, err)
			continue
		}
		if b.RefCount != 0 {
			t.Fatalf("%s ref_count=%d want=0", d, b.RefCount)
		}
	}
}

// TestAutoScanEnqueuedOnPush: when repo.auto_scan is true, a scans row lands.
// Needs a rootfs-bearing manifest — P-1 skips enqueue for non-scannable
// manifests (attestation, index, empty layers).
func TestAutoScanEnqueuedOnPush(t *testing.T) {
	f := newManifestFixture(t, true /* autoScan */)
	cfg := f.seedBlob([]byte("cfg"))
	layer := f.seedBlob([]byte("layer-bytes"))
	body := buildManifest(cfg, layer)
	resp := f.putManifest("v1", body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("put: %d", resp.StatusCode)
	}
	// Inspect scans table directly.
	var n int
	if err := f.db.Reader.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM scans WHERE repo_id=? AND artifact_kind='docker'`,
		f.repoID).Scan(&n); err != nil {
		t.Fatalf("count scans: %v", err)
	}
	if n != 1 {
		t.Fatalf("scan rows=%d; want 1", n)
	}
	if f.kickCount == 0 {
		t.Fatal("scanKick not invoked")
	}
}

// P-1 enqueue-side: attestation manifests must NOT produce a scan row even
// when the repo has auto_scan on. Pairs with
// scan.TestHandler_AttestationManifest_SkipsRunnerAndMarksDone, which is
// the handler-side belt-and-braces defense.
func TestAutoScanSkipsAttestationManifest(t *testing.T) {
	f := newManifestFixture(t, true /* autoScan */)
	cfg := f.seedBlob([]byte("cfg"))
	attLayer := f.seedBlob([]byte(`{"_type":"in-toto-stub"}`))
	body, _ := json.Marshal(map[string]any{
		"schemaVersion": 2,
		"mediaType":     oci.MediaTypeOCIManifest,
		"config": map[string]any{
			"mediaType": "application/vnd.oci.image.config.v1+json",
			"digest":    cfg,
			"size":      50,
		},
		"layers": []map[string]any{
			{
				"mediaType": "application/vnd.in-toto+json",
				"digest":    attLayer,
				"size":      100,
			},
		},
	})
	resp := f.putManifest("v1-attest", body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("put: %d", resp.StatusCode)
	}
	var n int
	if err := f.db.Reader.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM scans WHERE repo_id=? AND artifact_kind='docker'`,
		f.repoID).Scan(&n); err != nil {
		t.Fatalf("count scans: %v", err)
	}
	if n != 0 {
		t.Fatalf("scan rows=%d; want 0 (attestation must not enqueue)", n)
	}
}

// TestManifestPutEmitsAuditEvent : oci.manifest.uploaded recorded.
func TestManifestPutEmitsAuditEvent(t *testing.T) {
	f := newManifestFixture(t, false)
	cfg := f.seedBlob([]byte("cfg"))
	body := buildManifest(cfg)
	resp := f.putManifest("v1", body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("put: %d", resp.StatusCode)
	}
	found := false
	for _, k := range f.audit.kinds() {
		if k == string(audit.EvtOCIManifestUploaded) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("no oci.manifest.uploaded audit event; kinds=%v", f.audit.kinds())
	}
}

// TestManifestDelete_MalformedBodyRejected is the WR-04 regression. If a
// manifest's stored body has become unparseable (corruption, migration bug),
// the previous code silently ignored the parse error, ran zero ref
// decrements, and deleted the manifest row — orphaning blobs with inflated
// ref_counts that GC could never reclaim. The fix must return
// MANIFEST_INVALID and leave the row intact.
func TestManifestDelete_MalformedBodyRejected(t *testing.T) {
	f := newManifestFixture(t, false)
	// Seed a manifest row with malformed body bytes directly via the repo.
	// We skip the PUT path because that would reject the bad JSON upfront.
	garbage := []byte("this is not json at all {{{")
	sum := sha256.Sum256(garbage)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	if err := f.db.WriteTx(context.Background(), func(tx *sql.Tx) error {
		return f.manifests.Insert(context.Background(), tx, f.repoID, digest,
			oci.MediaTypeOCIManifest, garbage)
	}); err != nil {
		t.Fatalf("seed malformed manifest: %v", err)
	}

	resp := f.deleteManifest(digest)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("delete malformed: status=%d want 400 body=%s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "MANIFEST_INVALID") {
		t.Fatalf("body missing MANIFEST_INVALID: %s", body)
	}
	// Row MUST still be there — the DELETE aborted before tx open.
	m, err := f.manifests.GetByDigest(context.Background(), f.repoID, digest)
	if err != nil || m == nil {
		t.Fatalf("manifest row gone after failed delete (err=%v m=%+v) — silent loss!", err, m)
	}
}

// TestManifestPutDigestRefMismatch : URL digest != body sha256 → 400.
func TestManifestPutDigestRefMismatch(t *testing.T) {
	f := newManifestFixture(t, false)
	cfg := f.seedBlob([]byte("cfg"))
	body := buildManifest(cfg)
	wrongDigest := "sha256:" + strings.Repeat("a", 64)
	resp := f.putManifest(wrongDigest, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("digest mismatch: status %d; want 400", resp.StatusCode)
	}
}
