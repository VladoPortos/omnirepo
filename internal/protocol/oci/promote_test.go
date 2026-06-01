package oci_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/vladoportos/omnirepo/internal/audit"
	"github.com/vladoportos/omnirepo/internal/auth"
	omrcrypto "github.com/vladoportos/omnirepo/internal/crypto"
	"github.com/vladoportos/omnirepo/internal/metadata"
	"github.com/vladoportos/omnirepo/internal/protocol/oci"
)

// silence unused import warnings for helpers used by pull_external_test.go
var _ = io.Discard
var _ strings.Builder

// newTestAEAD builds an AEAD from a fixed 32-byte key. Shared by tests in
// this package that need to spin up a UpstreamCredsRepo.
func newTestAEAD(key []byte) (*omrcrypto.AEAD, error) {
	return omrcrypto.New(key)
}

// newChiRouterForTests returns a chi.Router with no middleware. Callers
// mount handlers directly; tests supply actors via ctx.
func newChiRouterForTests() chi.Router {
	return chi.NewRouter()
}

// promoteFixture extends manifestFixture with a dst repo (same project) and
// the PromoteREST handler.
type promoteFixture struct {
	*manifestFixture
	dstRepoID   int64
	dstRepoPath string
	promote     *oci.PromoteREST
}

func newPromoteFixture(t *testing.T) *promoteFixture {
	t.Helper()
	mf := newManifestFixture(t, false)
	// Create a destination repo in the same project.
	dstID, err := mf.repos.Create(context.Background(), mf.projectID,
		"docker", "app2", "", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
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
	return &promoteFixture{
		manifestFixture: mf,
		dstRepoID:       dstID,
		dstRepoPath:     "proj/docker/app2",
		promote:         oci.NewPromoteREST(ociH),
	}
}

// pushAndTag uploads a test manifest to the src repo at tag=srcTag, returns
// the manifest digest.
func (p *promoteFixture) pushAndTag(t *testing.T, srcTag string) string {
	t.Helper()
	configDig := p.seedBlob([]byte("config-bytes"))
	layerDig := p.seedBlob([]byte("layer-bytes"))
	body := buildManifest(configDig, layerDig)
	resp := p.putManifest(srcTag, body)
	defer resp.Body.Close()
	if resp.StatusCode != 201 {
		t.Fatalf("put manifest: %d", resp.StatusCode)
	}
	return resp.Header.Get("Docker-Content-Digest")
}

// countBlobFiles returns the number of files under <dataRoot>/blobs/sha256.
func (p *promoteFixture) countBlobFiles(t *testing.T) int {
	t.Helper()
	root := filepath.Join(p.dataRoot, "blobs", "sha256")
	count := 0
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err == nil && info != nil && !info.IsDir() {
			count++
		}
		return nil
	})
	return count
}

// doPromote posts a JSON body with actor on ctx + membership for projectID.
func (p *promoteFixture) doPromote(actor auth.Actor, req oci.PromoteRequest, memberships ...int64) *httptest.ResponseRecorder {
	buf, _ := json.Marshal(req)
	httpReq := httptest.NewRequest("POST",
		fmt.Sprintf("/api/v1/projects/%s/repos/docker/%s/promote", "proj", "app"),
		bytes.NewReader(buf))
	ctx := auth.WithActor(httpReq.Context(), actor)
	if len(memberships) > 0 {
		m := make(map[int64]string, len(memberships))
		for _, pid := range memberships {
			m[pid] = "maintainer"
		}
		ctx = auth.WithProjectMembership(ctx, m)
	}
	httpReq = httpReq.WithContext(ctx)
	rr := httptest.NewRecorder()
	router := newChiRouterForTests()
	router.Post("/api/v1/projects/{name}/repos/docker/{repo}/promote", p.promote.Handle)
	router.ServeHTTP(rr, httpReq)
	return rr
}

// TestPromote_ZeroBlobCopy: CAS file count before==after; each referenced
// blob's ref_count increments by exactly 1.
func TestPromote_ZeroBlobCopy(t *testing.T) {
	p := newPromoteFixture(t)
	// Actually push a real manifest (with real blobs uploaded, not just
	// seeded refcount rows) so the blobs/sha256 directory has files.
	// We use the /v2 blob upload path via the manifestFixture.
	configBytes := []byte("config-bytes-promote")
	layerBytes := []byte("layer-bytes-promote")
	configDig := p.uploadBlob(t, configBytes)
	layerDig := p.uploadBlob(t, layerBytes)
	body := buildManifestReal(configDig, layerDig, len(configBytes), len(layerBytes))
	resp := p.putManifest("prod", body)
	resp.Body.Close()
	if resp.StatusCode != 201 {
		t.Fatalf("put manifest: %d", resp.StatusCode)
	}
	digest := resp.Header.Get("Docker-Content-Digest")

	countBefore := p.countBlobFiles(t)

	// Snapshot ref_counts on referenced blobs.
	before := map[string]int64{}
	for _, d := range []string{configDig, layerDig} {
		b, _ := p.blobs.Stat(context.Background(), d)
		if b == nil {
			t.Fatalf("missing blob row %s", d)
		}
		before[d] = b.RefCount
	}

	// Resolve actor and promote.
	u, _ := metadata.NewUsersRepo(p.db).FindByLogin(context.Background(), p.login)
	actor := auth.Actor{ID: u.ID, Login: u.Login, Kind: auth.ActorKindUser}

	rr := p.doPromote(actor, oci.PromoteRequest{
		SrcTag:     "prod",
		DstProject: "proj",
		DstRepo:    "app2",
		DstTag:     "prod-clone",
	}, p.projectID)
	if rr.Code != 200 {
		t.Fatalf("promote status %d body=%s", rr.Code, rr.Body.String())
	}

	// CAS unchanged.
	countAfter := p.countBlobFiles(t)
	if countBefore != countAfter {
		t.Fatalf("CAS file count changed: before=%d after=%d", countBefore, countAfter)
	}

	// Each ref_count bumped by exactly 1.
	for _, d := range []string{configDig, layerDig} {
		b, _ := p.blobs.Stat(context.Background(), d)
		if b == nil {
			t.Fatalf("blob %s disappeared", d)
		}
		want := before[d] + 1
		if b.RefCount != want {
			t.Fatalf("blob %s ref_count=%d; want %d", d, b.RefCount, want)
		}
	}

	// dst repo has the same manifest body.
	dm, _ := p.manifests.GetByDigest(context.Background(), p.dstRepoID, digest)
	if dm == nil {
		t.Fatal("dst manifest missing")
	}
	if !bytes.Equal(dm.Body, body) {
		t.Fatal("dst body mismatch with src")
	}

	// dst tag resolves.
	got, _ := p.tags.Resolve(context.Background(), p.dstRepoID, "", "prod-clone")
	if got != digest {
		t.Fatalf("dst tag digest=%s want %s", got, digest)
	}

	// Audit oci.promote must be emitted.
	var found bool
	for _, k := range p.audit.kinds() {
		if k == string(audit.EvtOCIPromote) {
			found = true
		}
	}
	if !found {
		t.Fatalf("no oci.promote audit; got %v", p.audit.kinds())
	}
}

// TestPromote_NonMember_Returns403: an actor who is not a member of the dst
// project is denied with 403.
func TestPromote_NonMember_Returns403(t *testing.T) {
	p := newPromoteFixture(t)
	// Create a second project with a separate repo.
	otherProjID, err := p.projects.Create(context.Background(), "other", "x")
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.repos.Create(context.Background(), otherProjID, "docker", "dst", "", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Push a manifest into the src repo so there's something to promote.
	_ = p.pushAndTag(t, "source")

	// Actor is a member of 'proj' but not 'other'.
	u, _ := metadata.NewUsersRepo(p.db).FindByLogin(context.Background(), p.login)
	actor := auth.Actor{ID: u.ID, Login: u.Login, Kind: auth.ActorKindUser}

	rr := p.doPromote(actor, oci.PromoteRequest{
		SrcTag:     "source",
		DstProject: "other",
		DstRepo:    "dst",
		DstTag:     "dst-tag",
	}, p.projectID) // membership set = src only; missing 'other'
	if rr.Code != 403 {
		t.Fatalf("status %d; want 403 body=%s", rr.Code, rr.Body.String())
	}
}

// TestPromote_SrcTagNotFound_Returns404.
func TestPromote_SrcTagNotFound_Returns404(t *testing.T) {
	p := newPromoteFixture(t)
	u, _ := metadata.NewUsersRepo(p.db).FindByLogin(context.Background(), p.login)
	actor := auth.Actor{ID: u.ID, Login: u.Login, Kind: auth.ActorKindUser}
	rr := p.doPromote(actor, oci.PromoteRequest{
		SrcTag:     "does-not-exist",
		DstProject: "proj",
		DstRepo:    "app2",
		DstTag:     "never",
	}, p.projectID)
	if rr.Code != 404 {
		t.Fatalf("status %d; want 404", rr.Code)
	}
}

// -----------------------------------------------------------------------------
// Blob-upload helpers for tests that need real CAS content (TestPromote_ZeroBlobCopy).
// -----------------------------------------------------------------------------

// uploadBlob drives the full POST→PATCH→PUT chunked upload sequence for
// content, returning the resulting digest.
func (p *promoteFixture) uploadBlob(t *testing.T, content []byte) string {
	t.Helper()
	// POST /blobs/uploads/
	postURL := fmt.Sprintf("%s/v2/%s/blobs/uploads/", p.srv.URL, p.repoPath)
	req, _ := http.NewRequest("POST", postURL, nil)
	req.Header.Set("Authorization", "Bearer "+p.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST upload: %v", err)
	}
	if resp.StatusCode != 202 {
		resp.Body.Close()
		t.Fatalf("POST upload: %d", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	resp.Body.Close()
	if loc == "" {
		t.Fatal("no Location")
	}
	patchURL := loc
	if !strings.HasPrefix(loc, "http") {
		patchURL = p.srv.URL + loc
	}

	// PATCH content.
	pReq, _ := http.NewRequest("PATCH", patchURL, bytes.NewReader(content))
	pReq.Header.Set("Authorization", "Bearer "+p.token)
	pReq.Header.Set("Content-Type", "application/octet-stream")
	pResp, err := http.DefaultClient.Do(pReq)
	if err != nil {
		t.Fatalf("PATCH: %v", err)
	}
	if pResp.StatusCode != 202 {
		pResp.Body.Close()
		t.Fatalf("PATCH: %d", pResp.StatusCode)
	}
	pResp.Body.Close()

	// PUT with digest.
	digest := digestOf(content)
	putURL := patchURL
	if strings.Contains(putURL, "?") {
		putURL += "&digest=" + digest
	} else {
		putURL += "?digest=" + digest
	}
	uReq, _ := http.NewRequest("PUT", putURL, nil)
	uReq.Header.Set("Authorization", "Bearer "+p.token)
	uResp, err := http.DefaultClient.Do(uReq)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	if uResp.StatusCode != 201 {
		uResp.Body.Close()
		t.Fatalf("PUT: %d", uResp.StatusCode)
	}
	uResp.Body.Close()
	return digest
}

// buildManifestReal builds a manifest with accurate sizes.
func buildManifestReal(configDigest, layerDigest string, configSize, layerSize int) []byte {
	m := map[string]any{
		"schemaVersion": 2,
		"mediaType":     oci.MediaTypeOCIManifest,
		"config": map[string]any{
			"mediaType": "application/vnd.oci.image.config.v1+json",
			"digest":    configDigest,
			"size":      configSize,
		},
		"layers": []map[string]any{{
			"mediaType": "application/vnd.oci.image.layer.v1.tar+gzip",
			"digest":    layerDigest,
			"size":      layerSize,
		}},
	}
	b, _ := json.Marshal(m)
	return b
}
