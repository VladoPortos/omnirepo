package oci_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/dxc-internal/omnirepo/internal/audit"
	"github.com/dxc-internal/omnirepo/internal/auth"
	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/protocol/oci"
)

// -----------------------------------------------------------------------------
// sanitizeUpstreamErr unit tests — we test the exported behaviour indirectly
// by observing the error that comes back from Handle on a malformed cred.
// -----------------------------------------------------------------------------

// TestSanitizeUpstreamErr_ScrubsAuthHeader: exercise the exported Sanitize
// helper (we expose it as a package-level function for this test; if not
// exported, this test documents the required behaviour and runs against the
// observable surface).
func TestSanitizeUpstreamErr_ScrubsAuthHeader(t *testing.T) {
	// The function is unexported; assert behaviour via a constructed error
	// that would contain the header string and flowing through the regex.
	orig := errors.New(`request failed: header "Authorization: Basic YWxpY2U6c2VjcmV0" rejected`)
	scrubbed := oci.SanitizeUpstreamErrExported(orig)
	if scrubbed == nil {
		t.Fatal("scrubbed nil")
	}
	if strings.Contains(scrubbed.Error(), "YWxpY2U6c2VjcmV0") {
		t.Fatalf("credential not scrubbed: %q", scrubbed.Error())
	}
	if !strings.Contains(scrubbed.Error(), "REDACTED") {
		t.Fatalf("no REDACTED marker: %q", scrubbed.Error())
	}
}

// -----------------------------------------------------------------------------
// Mock upstream registry — implements the minimum /v2 surface needed by
// go-containerregistry/pkg/v1/remote for a single-image pull:
//
//   GET  /v2/                           → 200 ping
//   GET  /v2/<name>/manifests/<ref>     → 200 with stored body+media type
//   GET  /v2/<name>/blobs/<digest>      → 200 with stored bytes
//
// Tag resolution: <ref> may be a tag name or a digest. Tests seed one tag
// ("v1") pointing at the manifest digest.
// -----------------------------------------------------------------------------

type mockUpstream struct {
	t          *testing.T
	requireBasic bool
	user       string
	pass       string
	manifestBody []byte
	manifestMT   string
	manifestDigest string
	blobs      map[string][]byte // digest -> bytes
	tag        string            // tag name, e.g. "v1"
	imageName  string            // e.g. "lib/app"
	srv        *httptest.Server
	// first500 flips true after the first manifest GET; used by the retry
	// test to simulate a transient upstream error.
	first500 bool
}

func newMockUpstream(t *testing.T, imageName, tag string, requireBasic bool) *mockUpstream {
	t.Helper()
	m := &mockUpstream{
		t:            t,
		requireBasic: requireBasic,
		user:         "alice",
		pass:         "sekret",
		imageName:    imageName,
		tag:          tag,
		blobs:        map[string][]byte{},
	}
	// Build a minimal OCI image manifest referencing one config + one layer.
	configBytes := []byte(`{"architecture":"amd64","os":"linux","rootfs":{"type":"layers","diff_ids":["sha256:abc"]}}`)
	layerBytes := []byte("hello-layer-bytes-42")
	m.blobs[digestOf(configBytes)] = configBytes
	m.blobs[digestOf(layerBytes)] = layerBytes

	manifest := map[string]any{
		"schemaVersion": 2,
		"mediaType":     "application/vnd.oci.image.manifest.v1+json",
		"config": map[string]any{
			"mediaType": "application/vnd.oci.image.config.v1+json",
			"digest":    digestOf(configBytes),
			"size":      len(configBytes),
		},
		"layers": []map[string]any{{
			"mediaType": "application/vnd.oci.image.layer.v1.tar+gzip",
			"digest":    digestOf(layerBytes),
			"size":      len(layerBytes),
		}},
	}
	mb, _ := json.Marshal(manifest)
	m.manifestBody = mb
	m.manifestMT = "application/vnd.oci.image.manifest.v1+json"
	m.manifestDigest = digestOf(mb)

	mux := http.NewServeMux()
	mux.HandleFunc("/v2/", m.handle)
	m.srv = httptest.NewServer(mux)
	t.Cleanup(m.srv.Close)
	return m
}

func (m *mockUpstream) handle(w http.ResponseWriter, r *http.Request) {
	if m.requireBasic {
		u, p, ok := r.BasicAuth()
		if !ok || u != m.user || p != m.pass {
			w.Header().Set("WWW-Authenticate", `Basic realm="mock"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	}
	path := r.URL.Path
	if path == "/v2/" || path == "/v2" {
		w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
		w.WriteHeader(http.StatusOK)
		return
	}
	// /v2/<name>/manifests/<ref>
	if strings.Contains(path, "/manifests/") {
		ref := path[strings.LastIndex(path, "/")+1:]
		// tag-or-digest both resolve to the same manifest for this mock
		if ref != m.tag && ref != m.manifestDigest {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", m.manifestMT)
		w.Header().Set("Docker-Content-Digest", m.manifestDigest)
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(m.manifestBody)))
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		_, _ = w.Write(m.manifestBody)
		return
	}
	if strings.Contains(path, "/blobs/") {
		digest := path[strings.LastIndex(path, "/")+1:]
		b, ok := m.blobs[digest]
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Docker-Content-Digest", digest)
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(b)))
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		_, _ = w.Write(b)
		return
	}
	http.Error(w, "not implemented", http.StatusNotImplemented)
}

// srcImageRef returns the upstream reference string for the mock image.
func (m *mockUpstream) srcImageRef() string {
	u, _ := url.Parse(m.srv.URL)
	return fmt.Sprintf("%s/%s:%s", u.Host, m.imageName, m.tag)
}

func (m *mockUpstream) registryHost() string {
	u, _ := url.Parse(m.srv.URL)
	return u.Host
}

func digestOf(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// -----------------------------------------------------------------------------
// Pull-external job handler tests
// -----------------------------------------------------------------------------

// pullFixture wires a manifestFixture + a mock upstream + a PullExternalHandler.
type pullFixture struct {
	*manifestFixture
	up   *mockUpstream
	pull *oci.PullExternalHandler
	creds *metadata.UpstreamCredsRepo
}

func newPullFixture(t *testing.T, requireBasic bool) *pullFixture {
	t.Helper()
	mf := newManifestFixture(t, false)
	up := newMockUpstream(t, "lib/app", "v1", requireBasic)

	// UpstreamCredsRepo needs an AEAD key; build a throwaway one.
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	aead, err := newTestAEAD(key)
	if err != nil {
		t.Fatal(err)
	}
	credsRepo := metadata.NewUpstreamCredsRepo(mf.db, aead)

	// Build a synthetic OCI Handler shim that shares the same wiring as the
	// /v2 handler mounted in the manifestFixture. We construct a second
	// handler instance with identical deps so the PullExternalHandler's
	// writeManifestWithRefcounts logic runs against the test DB.
	ociH := oci.New(oci.Deps{
		DB:          mf.db,
		Users:       metadata.NewUsersRepo(mf.db),
		APIKeys:     metadata.NewAPIKeysRepo(mf.db),
		Repos:       mf.repos,
		Projects:    mf.projects,
		Sessions:    metadata.NewSessionsRepo(mf.db),
		Members:     mf.members,
		CAS:         mf.cas,
		Blobs:       mf.blobs,
		BlobUploads: metadata.NewBlobUploadsRepo(mf.db),
		Sess:        metadata.NewBlobUploadSessionsRepo(mf.db),
		Audit:       mf.audit,
		DataRoot:    mf.dataRoot,
		HMACSecret:  []byte("0123456789abcdef0123456789abcdef"),
		JWTTTL:      time.Hour,
		Manifests:   mf.manifests,
		Tags:        mf.tags,
		Scans:       mf.scans,
		ScanKick:    func() {},
	})

	pull := oci.NewPullExternalHandler(oci.PullExternalDeps{
		DB:        mf.db,
		CAS:       mf.cas,
		Blobs:     mf.blobs,
		Manifests: mf.manifests,
		Tags:      mf.tags,
		Scans:     mf.scans,
		ScanKick:  func() {},
		Repos:     mf.repos,
		Projects:  mf.projects,
		Creds:     credsRepo,
		Audit:     mf.audit,
		OCI:       ociH,
		Timeout:   30 * time.Second,
	})
	return &pullFixture{manifestFixture: mf, up: up, pull: pull, creds: credsRepo}
}

// runPull invokes the handler directly with a constructed payload + repoID.
func (f *pullFixture) runPull(job oci.PullExternalJob) error {
	buf, _ := json.Marshal(&job)
	return f.pull.Handle(context.Background(), string(buf), f.projectID, f.repoID)
}

// TestPullExternal_Anonymous_ImportsManifestByteIdentical: the mock upstream
// allows anonymous; after the job runs, the local manifest body for the
// upstream digest must equal the upstream body byte-for-byte (Pitfall 5).
func TestPullExternal_Anonymous_ImportsManifestByteIdentical(t *testing.T) {
	f := newPullFixture(t, false /* anonymous upstream */)
	err := f.runPull(oci.PullExternalJob{
		SrcImage: f.up.srcImageRef(),
		DstTag:   "pulled",
	})
	if err != nil {
		t.Fatalf("runPull: %v", err)
	}
	// Local manifest by upstream digest must match upstream body.
	m, err := f.manifests.GetByDigest(context.Background(), f.repoID, f.up.manifestDigest)
	if err != nil {
		t.Fatalf("get manifest: %v", err)
	}
	if m == nil {
		t.Fatal("manifest not stored locally")
	}
	if !bytes.Equal(m.Body, f.up.manifestBody) {
		t.Fatalf("body not byte-identical:\n  got : %q\n  want: %q", m.Body, f.up.manifestBody)
	}
	// Tag should point at mfDigest.
	d, err := f.tags.Resolve(context.Background(), f.repoID, "pulled")
	if err != nil {
		t.Fatalf("resolve tag: %v", err)
	}
	if d != f.up.manifestDigest {
		t.Fatalf("tag digest = %s; want %s", d, f.up.manifestDigest)
	}
}

// TestPullExternal_BasicAuth_ImportsViaCred: upstream requires Basic auth.
// The cred is stored in UpstreamCredsRepo and looked up by cred_id.
func TestPullExternal_BasicAuth_ImportsViaCred(t *testing.T) {
	f := newPullFixture(t, true /* Basic-required upstream */)
	credID, err := f.creds.Create(
		context.Background(), f.projectID,
		f.up.registryHost(), metadata.CredKindDocker,
		f.up.user, f.up.pass, "", 0,
	)
	if err != nil {
		t.Fatalf("create cred: %v", err)
	}
	if err := f.runPull(oci.PullExternalJob{
		SrcImage: f.up.srcImageRef(),
		DstTag:   "pulled-auth",
		CredID:   credID,
	}); err != nil {
		t.Fatalf("runPull (auth): %v", err)
	}
	m, _ := f.manifests.GetByDigest(context.Background(), f.repoID, f.up.manifestDigest)
	if m == nil {
		t.Fatal("manifest absent after auth pull")
	}
	if !bytes.Equal(m.Body, f.up.manifestBody) {
		t.Fatal("body mismatch after auth pull")
	}
}

// TestPullExternal_CredHostMismatch_JobReturnsError: cred host ≠ src host
// must surface as an error from Handle (the job will be retried by the
// pool's MarkFailed semantics).
func TestPullExternal_CredHostMismatch_JobReturnsError(t *testing.T) {
	f := newPullFixture(t, false)
	// Create a cred whose host intentionally differs from the upstream host.
	credID, err := f.creds.Create(
		context.Background(), f.projectID,
		"elsewhere.invalid", metadata.CredKindDocker,
		"u", "p", "", 0,
	)
	if err != nil {
		t.Fatalf("create cred: %v", err)
	}
	err = f.runPull(oci.PullExternalJob{
		SrcImage: f.up.srcImageRef(),
		DstTag:   "wont-happen",
		CredID:   credID,
	})
	if err == nil {
		t.Fatal("expected cred_host_mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), "cred_host_mismatch") {
		t.Fatalf("expected cred_host_mismatch, got: %v", err)
	}
}

// -----------------------------------------------------------------------------
// REST enqueue handler tests
// -----------------------------------------------------------------------------

// restFixture wires a pullFixture + the REST enqueue handler under a chi
// router that is entered with an Actor already on ctx (mimicking the
// api.Mount auth chain).
type restFixture struct {
	*pullFixture
	rest     *oci.PullExternalREST
	promote  *oci.PromoteREST
	jobsRepo *metadata.SyncJobsRepo
	kick     int
}

func newRestFixture(t *testing.T) *restFixture {
	pf := newPullFixture(t, false)
	jobsRepo := metadata.NewSyncJobsRepo(pf.db)
	rf := &restFixture{pullFixture: pf, jobsRepo: jobsRepo}

	// Build a second OCI handler for the REST wrapper (in real app.Run
	// the same ociHandler is passed both to /v2 and to the REST wrappers).
	ociH := oci.New(oci.Deps{
		DB:          pf.db,
		Users:       metadata.NewUsersRepo(pf.db),
		APIKeys:     metadata.NewAPIKeysRepo(pf.db),
		Repos:       pf.repos,
		Projects:    pf.projects,
		Sessions:    metadata.NewSessionsRepo(pf.db),
		Members:     pf.members,
		CAS:         pf.cas,
		Blobs:       pf.blobs,
		BlobUploads: metadata.NewBlobUploadsRepo(pf.db),
		Sess:        metadata.NewBlobUploadSessionsRepo(pf.db),
		Audit:       pf.audit,
		DataRoot:    pf.dataRoot,
		HMACSecret:  []byte("0123456789abcdef0123456789abcdef"),
		JWTTTL:      time.Hour,
		Manifests:   pf.manifests,
		Tags:        pf.tags,
		Scans:       pf.scans,
		ScanKick:    func() {},
	})
	rf.rest = oci.NewPullExternalREST(ociH, pf.creds, jobsRepo, func() { rf.kick++ })
	rf.promote = oci.NewPromoteREST(ociH)
	return rf
}

// doREST posts body to the pull-external endpoint with actor=user on ctx.
func (rf *restFixture) doPullExternal(actor auth.Actor, body any) *httptest.ResponseRecorder {
	buf, _ := json.Marshal(body)
	req := httptest.NewRequest("POST",
		fmt.Sprintf("/api/v1/projects/%s/repos/docker/%s/pull-external", "proj", "app"),
		bytes.NewReader(buf))
	// Simulate chi URL params by wrapping via a mini router.
	rr := httptest.NewRecorder()
	ctx := auth.WithActor(req.Context(), actor)
	// Attach membership for user actors so auth.Can project-scoped checks pass.
	if actor.Kind == auth.ActorKindUser && actor.ID != 0 {
		ctx = auth.WithProjectMembership(ctx, []int64{rf.projectID})
	}
	req = req.WithContext(ctx)

	router := rf.pullExternalRouter()
	router.ServeHTTP(rr, req)
	return rr
}

func (rf *restFixture) pullExternalRouter() http.Handler {
	// Use the real chi router pattern the app uses.
	r := newChiRouterForTests()
	r.Post("/api/v1/projects/{name}/repos/docker/{repo}/pull-external", rf.rest.Handle)
	r.Post("/api/v1/projects/{name}/repos/docker/{repo}/promote", rf.promote.Handle)
	return r
}

// TestPullExternalREST_HappyPath_Enqueues202: authenticated member posts a
// valid body → 202 with job_id; sync_jobs row exists with kind=pull_external.
func TestPullExternalREST_HappyPath_Enqueues202(t *testing.T) {
	rf := newRestFixture(t)
	// Resolve the member login's user ID from the fixture.
	u, err := metadata.NewUsersRepo(rf.db).FindByLogin(context.Background(), rf.login)
	if err != nil {
		t.Fatal(err)
	}
	actor := auth.Actor{ID: u.ID, Login: u.Login, Kind: auth.ActorKindUser}

	rr := rf.doPullExternal(actor, oci.PullExternalRequest{
		SrcImage: rf.up.srcImageRef(),
		DstTag:   "imported",
	})
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status %d body=%s", rr.Code, rr.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if _, ok := resp["job_id"]; !ok {
		t.Fatalf("no job_id in response: %s", rr.Body.String())
	}
	if rf.kick == 0 {
		t.Fatal("pool Kick not called")
	}
	// Verify the job row exists.
	jobsRepo := rf.jobsRepo
	// Lease the pending row via the leaser and confirm it's our kind.
	j, ok, err := jobsRepo.LeaseOne(context.Background(), "test-lease")
	if err != nil || !ok {
		t.Fatalf("lease: ok=%v err=%v", ok, err)
	}
	if j.Kind != oci.PullExternalJobKind {
		t.Fatalf("job kind=%q; want pull_external", j.Kind)
	}
	var jp oci.PullExternalJob
	if err := json.Unmarshal([]byte(j.PayloadJSON), &jp); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if jp.SrcImage != rf.up.srcImageRef() || jp.DstTag != "imported" {
		t.Fatalf("payload mismatch: %+v", jp)
	}
	// Audit event EvtOCIPullExternalStarted must have been recorded.
	var found bool
	for _, k := range rf.audit.kinds() {
		if k == string(audit.EvtOCIPullExternalStarted) {
			found = true
		}
	}
	if !found {
		t.Fatalf("no pull_external.started audit; got %v", rf.audit.kinds())
	}
}

// TestPullExternalREST_CredHostMismatch_Returns400: REST catches the
// mismatch synchronously.
func TestPullExternalREST_CredHostMismatch_Returns400(t *testing.T) {
	rf := newRestFixture(t)
	u, _ := metadata.NewUsersRepo(rf.db).FindByLogin(context.Background(), rf.login)
	actor := auth.Actor{ID: u.ID, Login: u.Login, Kind: auth.ActorKindUser}
	credID, err := rf.creds.Create(context.Background(), rf.projectID,
		"wrong.invalid", metadata.CredKindDocker, "u", "p", "", u.ID)
	if err != nil {
		t.Fatal(err)
	}
	rr := rf.doPullExternal(actor, oci.PullExternalRequest{
		SrcImage: rf.up.srcImageRef(),
		CredID:   credID,
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status %d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "cred_host_mismatch") {
		t.Fatalf("expected cred_host_mismatch, got: %s", rr.Body.String())
	}
}

// TestPullExternalREST_Unauthenticated_Returns401: no actor on ctx → 401.
func TestPullExternalREST_Unauthenticated_Returns401(t *testing.T) {
	rf := newRestFixture(t)
	// Send with an anonymous actor.
	rr := rf.doPullExternal(auth.Actor{Kind: auth.ActorKindAnonymous},
		oci.PullExternalRequest{SrcImage: rf.up.srcImageRef()})
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status %d; want 401", rr.Code)
	}
}

// TestPullExternalREST_NonMember_Returns403: actor exists but isn't a member
// of the target project → 403.
func TestPullExternalREST_NonMember_Returns403(t *testing.T) {
	rf := newRestFixture(t)
	// Build an unrelated user and do NOT add them to membership.
	users := metadata.NewUsersRepo(rf.db)
	pwHash, _ := auth.HashPassword("stranger-pass-77")
	sid, err := users.Create(context.Background(), "stranger", "s@example.com", pwHash, false, false)
	if err != nil {
		t.Fatal(err)
	}
	actor := auth.Actor{ID: sid, Login: "stranger", Kind: auth.ActorKindUser}
	// Deliberately do NOT attach project membership → auth.Can returns
	// not_a_project_member.
	buf, _ := json.Marshal(oci.PullExternalRequest{SrcImage: rf.up.srcImageRef()})
	req := httptest.NewRequest("POST",
		fmt.Sprintf("/api/v1/projects/%s/repos/docker/%s/pull-external", "proj", "app"),
		bytes.NewReader(buf))
	req = req.WithContext(auth.WithActor(req.Context(), actor))
	rr := httptest.NewRecorder()
	rf.pullExternalRouter().ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status %d; want 403 body=%s", rr.Code, rr.Body.String())
	}
}

// -----------------------------------------------------------------------------
// helpers: newTestAEAD + newChiRouterForTests are defined in promote_test.go
// to keep both test files cohesive.
// -----------------------------------------------------------------------------

// Quiet unused io import — reserved for optional upstream-body asserts.
var _ = io.Discard
