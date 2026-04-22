package oci_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dxc-internal/omnirepo/internal/auth"
	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/protocol/oci"
)

// deleteTagFixture reuses the promoteFixture plumbing (it already seeds a
// manifest handler + tags/manifests repos + a test project) and layers on
// the DeleteTagREST handler.
type deleteTagFixture struct {
	*promoteFixture
	deleteTag *oci.DeleteTagREST
}

func newDeleteTagFixture(t *testing.T) *deleteTagFixture {
	t.Helper()
	p := newPromoteFixture(t)
	// Build a second OCI handler solely to wire DeleteTagREST against the
	// same repos/DB as the manifest fixture. In prod app.Run we reuse the
	// same ociHandler instance; here we just need the handler's private
	// helpers (canOnRepo, decRefs, manifestRefs) bound to a Handler with
	// these deps.
	h := oci.New(oci.Deps{
		DB:          p.db,
		Users:       metadata.NewUsersRepo(p.db),
		APIKeys:     metadata.NewAPIKeysRepo(p.db),
		Repos:       p.repos,
		Projects:    p.projects,
		Sessions:    metadata.NewSessionsRepo(p.db),
		Members:     p.members,
		CAS:         p.cas,
		Blobs:       p.blobs,
		BlobUploads: metadata.NewBlobUploadsRepo(p.db),
		Sess:        metadata.NewBlobUploadSessionsRepo(p.db),
		Audit:       p.audit,
		DataRoot:    p.dataRoot,
		HMACSecret:  []byte("0123456789abcdef0123456789abcdef"),
		JWTTTL:      time.Hour,
		Manifests:   p.manifests,
		Tags:        p.tags,
		Scans:       p.scans,
		ScanKick:    func() {},
	})
	return &deleteTagFixture{
		promoteFixture: p,
		deleteTag:      oci.NewDeleteTagREST(h),
	}
}

func (d *deleteTagFixture) do(actor auth.Actor, tag string, memberships ...int64) *httptest.ResponseRecorder {
	httpReq := httptest.NewRequest("DELETE",
		"/api/v1/projects/proj/repos/docker/app/tags/"+tag, nil)
	ctx := auth.WithActor(httpReq.Context(), actor)
	if len(memberships) > 0 {
		ctx = auth.WithProjectMembership(ctx, memberships)
	}
	httpReq = httpReq.WithContext(ctx)
	rr := httptest.NewRecorder()
	router := newChiRouterForTests()
	router.Delete("/api/v1/projects/{name}/repos/docker/{repo}/tags/{tag}", d.deleteTag.Handle)
	router.ServeHTTP(rr, httpReq)
	return rr
}

// TestDeleteTag_LastTag_CascadesToManifestDelete verifies the F-05.4 REST
// shim walks the same path as /v2 manifestDelete's tag-form cascade:
// tag unlink → ref_count check → full manifest delete when the tag was the
// last reference.
func TestDeleteTag_LastTag_CascadesToManifestDelete(t *testing.T) {
	d := newDeleteTagFixture(t)
	digest := d.pushAndTag(t, "v1")

	ctx := context.Background()
	u, _ := metadata.NewUsersRepo(d.db).FindByLogin(ctx, d.login)
	actor := auth.Actor{ID: u.ID, Login: u.Login, Kind: auth.ActorKindUser}

	rr := d.do(actor, "v1", d.projectID)
	if rr.Code != 200 {
		t.Fatalf("delete tag status %d body=%s", rr.Code, rr.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, rr.Body.String())
	}
	if resp["deleted"] != true {
		t.Fatalf("deleted=%v, want true", resp["deleted"])
	}
	if resp["digest"] != digest {
		t.Fatalf("digest=%v, want %s", resp["digest"], digest)
	}
	// Tag should be gone.
	dig, err := d.tags.Resolve(ctx, d.repoID, "", "v1")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if dig != "" {
		t.Fatalf("tag v1 still resolves to %q", dig)
	}
	// Manifest should have cascaded away too — no other tag refs it.
	m, _ := d.manifests.GetByDigest(ctx, d.repoID, digest)
	if m != nil {
		t.Fatalf("manifest still present after last-tag delete")
	}
}

// TestDeleteTag_SiblingTagKeepsManifest: when a second tag v2 points at
// the same digest as v1 (pushAndTag reuses config-bytes/layer-bytes, so
// consecutive pushes yield identical digests), deleting v1 must leave the
// underlying manifest row in place — mirrors manifestDelete's tagForm
// count-check short-circuit at manifests.go:542.
func TestDeleteTag_SiblingTagKeepsManifest(t *testing.T) {
	d := newDeleteTagFixture(t)
	digestV1 := d.pushAndTag(t, "v1")
	digestV2 := d.pushAndTag(t, "v2")
	if digestV1 != digestV2 {
		t.Fatalf("fixture changed: expected consecutive pushAndTag to yield same digest, got %s vs %s", digestV1, digestV2)
	}

	ctx := context.Background()
	u, _ := metadata.NewUsersRepo(d.db).FindByLogin(ctx, d.login)
	actor := auth.Actor{ID: u.ID, Login: u.Login, Kind: auth.ActorKindUser}

	rr := d.do(actor, "v1", d.projectID)
	if rr.Code != 200 {
		t.Fatalf("delete tag v1 status %d body=%s", rr.Code, rr.Body.String())
	}
	// Manifest must still be present because v2 still references it.
	m, err := d.manifests.GetByDigest(ctx, d.repoID, digestV1)
	if err != nil {
		t.Fatalf("get shared manifest: %v", err)
	}
	if m == nil {
		t.Fatalf("manifest cascaded away despite v2 still pointing at it — F-05.4 guard broken")
	}
	// v2 must still resolve to the same digest; v1 must be gone.
	if dig, err := d.tags.Resolve(ctx, d.repoID, "", "v2"); err != nil || dig != digestV1 {
		t.Fatalf("v2 resolve got dig=%q err=%v, want %s", dig, err, digestV1)
	}
	if dig, err := d.tags.Resolve(ctx, d.repoID, "", "v1"); err != nil || dig != "" {
		t.Fatalf("v1 resolve got dig=%q err=%v, want empty", dig, err)
	}
}

func TestDeleteTag_NonMember_Forbidden(t *testing.T) {
	d := newDeleteTagFixture(t)
	d.pushAndTag(t, "v1")

	actor := auth.Actor{ID: 99999, Login: "stranger", Kind: auth.ActorKindUser}
	rr := d.do(actor, "v1") // no project membership
	if rr.Code != 403 {
		t.Fatalf("stranger expected 403, got %d (body=%s)", rr.Code, rr.Body.String())
	}
}

func TestDeleteTag_UnknownTag_404(t *testing.T) {
	d := newDeleteTagFixture(t)
	d.pushAndTag(t, "v1")

	ctx := context.Background()
	u, _ := metadata.NewUsersRepo(d.db).FindByLogin(ctx, d.login)
	actor := auth.Actor{ID: u.ID, Login: u.Login, Kind: auth.ActorKindUser}

	rr := d.do(actor, "does-not-exist", d.projectID)
	if rr.Code != 404 {
		t.Fatalf("unknown tag expected 404, got %d (body=%s)", rr.Code, rr.Body.String())
	}
}
