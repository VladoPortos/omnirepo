package api_test

// Phase 11 Plan 03 Task 3 (OCIHELM-05 + D-04) — API-layer Docker Hub
// credential gate on POST /api/v1/projects/{name}/repos and PATCH
// /api/v1/projects/{name}/repos/helm/{repo}.
//
// The gate rejects (oci://registry-1.docker.io/*, no basic cred) with a
// 422 envelope code mirror.docker_hub_requires_credential — prevents
// operators from creating a Docker Hub helm mirror that would hit the
// 100 req / 6h anonymous cap on the first sync.
//
// Tests are hermetic: no Docker Hub network calls. They drive the HTTP
// handler via the existing api_test testServer harness and assert on
// response status + envelope code.

import (
	"context"
	"strings"
	"testing"

	"github.com/dxc-internal/omnirepo/internal/api"
	"github.com/dxc-internal/omnirepo/internal/metadata"
)

// TestCreateRepo_HelmMirror_DockerHub_NoCred_Returns422 — posting a
// helm mirror with a Docker Hub oci:// upstream and no cred_id MUST
// return 422 mirror.docker_hub_requires_credential BEFORE any row is
// inserted.
func TestCreateRepo_HelmMirror_DockerHub_NoCred_Returns422(t *testing.T) {
	s := newTestServer(t)
	cookie := bootProjectOnly(t, s, "p-dh")

	resp, body := s.do(t, "POST", "/api/v1/projects/p-dh/repos", cookie, map[string]any{
		"name":                "bitnami",
		"type":                "helm",
		"is_mirror":           true,
		"mirror_upstream_url": "oci://registry-1.docker.io/bitnamicharts/nginx",
		// mirror_cred_id intentionally omitted.
	})
	if resp.StatusCode != 422 {
		t.Fatalf("status = %d, want 422; body=%+v", resp.StatusCode, body)
	}
	code, _ := body["code"].(string)
	if code != "mirror.docker_hub_requires_credential" {
		t.Fatalf("code = %q, want mirror.docker_hub_requires_credential; body=%+v", code, body)
	}
	// Sanity: no repo row was inserted.
	ctx := context.Background()
	var count int64
	_ = s.db.Reader.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM repos WHERE name='bitnami'`).Scan(&count)
	if count != 0 {
		t.Errorf("repos row count = %d; want 0 (422 must refuse before insert)", count)
	}
}

// TestCreateRepo_HelmMirror_DockerHub_WithBasicCred_Succeeds — same URL
// with a basic cred attached succeeds. Note: the mirror_validate gate
// accepts "any non-empty cred kind" per plan 11-02 (D-06 locks kind to
// 'basic' for v1.4 semantically; the validator only enforces
// "something attached"), so a helm-kind cred unblocks too.
func TestCreateRepo_HelmMirror_DockerHub_WithBasicCred_Succeeds(t *testing.T) {
	s := newTestServerWithUpstream(t)
	cookie := bootProjectOnly(t, s, "p-dh-ok")

	// Seed a helm-kind upstream cred in p-dh-ok.
	ctx := context.Background()
	var pid int64
	_ = s.db.Reader.QueryRowContext(ctx, `SELECT id FROM projects WHERE name='p-dh-ok'`).Scan(&pid)
	credID, err := s.deps.UpstreamCreds.Create(ctx, pid, "registry-1.docker.io", metadata.CredKindHelm, "dockeruser", "pat-secret", "", 0)
	if err != nil {
		t.Fatalf("seed cred: %v", err)
	}

	resp, body := s.do(t, "POST", "/api/v1/projects/p-dh-ok/repos", cookie, map[string]any{
		"name":                "bitnami",
		"type":                "helm",
		"is_mirror":           true,
		"mirror_upstream_url": "oci://registry-1.docker.io/bitnamicharts/nginx",
		"mirror_cred_id":      credID,
	})
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200; body=%+v", resp.StatusCode, body)
	}
}

// TestCreateRepo_HelmMirror_GHCR_NoCred_Succeeds — the gate is Docker
// Hub-specific. GHCR (or any other OCI host) without a cred MUST NOT
// trip the 422.
func TestCreateRepo_HelmMirror_GHCR_NoCred_Succeeds(t *testing.T) {
	s := newTestServer(t)
	cookie := bootProjectOnly(t, s, "p-ghcr")

	resp, body := s.do(t, "POST", "/api/v1/projects/p-ghcr/repos", cookie, map[string]any{
		"name":                "ghcr-charts",
		"type":                "helm",
		"is_mirror":           true,
		"mirror_upstream_url": "oci://ghcr.io/foo/bar",
	})
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200 (ghcr without cred must bypass gate); body=%+v", resp.StatusCode, body)
	}
}

// TestPatchRepo_HelmMirror_RemoveDockerHubCred_Returns422 — start with a
// Docker Hub helm mirror that HAS a cred attached; PATCH to null-out the
// cred MUST return 422 mirror.docker_hub_requires_credential. The PATCH
// must NOT have cleared the cred on the DB row (transaction integrity).
func TestPatchRepo_HelmMirror_RemoveDockerHubCred_Returns422(t *testing.T) {
	s := newTestServerWithUpstream(t)
	cookie := bootProjectOnly(t, s, "p-patch")

	ctx := context.Background()
	var pid int64
	_ = s.db.Reader.QueryRowContext(ctx, `SELECT id FROM projects WHERE name='p-patch'`).Scan(&pid)
	credID, err := s.deps.UpstreamCreds.Create(ctx, pid, "registry-1.docker.io", metadata.CredKindHelm, "dockeruser", "pat-secret", "", 0)
	if err != nil {
		t.Fatalf("seed cred: %v", err)
	}

	// Create the helm mirror with the cred attached.
	if resp, body := s.do(t, "POST", "/api/v1/projects/p-patch/repos", cookie, map[string]any{
		"name":                "bitnami",
		"type":                "helm",
		"is_mirror":           true,
		"mirror_upstream_url": "oci://registry-1.docker.io/bitnamicharts/nginx",
		"mirror_cred_id":      credID,
	}); resp.StatusCode != 200 {
		t.Fatalf("initial create: %d %+v", resp.StatusCode, body)
	}

	// PATCH to null the cred — MUST be refused with 422.
	resp, body := s.do(t, "PATCH", "/api/v1/projects/p-patch/repos/helm/bitnami", cookie, map[string]any{
		"mirror_cred_id": nil,
	})
	if resp.StatusCode != 422 {
		t.Fatalf("status = %d, want 422; body=%+v", resp.StatusCode, body)
	}
	code, _ := body["code"].(string)
	if code != "mirror.docker_hub_requires_credential" {
		t.Fatalf("code = %q, want mirror.docker_hub_requires_credential; body=%+v", code, body)
	}
	// Persistent state: cred_id on the row must still point at the
	// original cred (the failing PATCH must not have been written).
	var storedCredID *int64
	_ = s.db.Reader.QueryRowContext(ctx,
		`SELECT mirror_cred_id FROM repos WHERE project_id=? AND name='bitnami'`, pid,
	).Scan(&storedCredID)
	if storedCredID == nil || *storedCredID != credID {
		t.Errorf("mirror_cred_id = %v; want %d (PATCH must have been rejected atomically)", storedCredID, credID)
	}
}

// TestPatchRepo_HelmMirror_DockerHub_WithSameCred_NoOp_Succeeds — the
// gate only fires when the effective state would leave a Docker Hub
// mirror cred-less. A PATCH that changes an unrelated editable field
// (scan_on_sync) MUST pass.
func TestPatchRepo_HelmMirror_DockerHub_WithSameCred_NoOp_Succeeds(t *testing.T) {
	s := newTestServerWithUpstream(t)
	cookie := bootProjectOnly(t, s, "p-patch-ok")

	ctx := context.Background()
	var pid int64
	_ = s.db.Reader.QueryRowContext(ctx, `SELECT id FROM projects WHERE name='p-patch-ok'`).Scan(&pid)
	credID, err := s.deps.UpstreamCreds.Create(ctx, pid, "registry-1.docker.io", metadata.CredKindHelm, "dockeruser", "pat-secret", "", 0)
	if err != nil {
		t.Fatalf("seed cred: %v", err)
	}
	if resp, body := s.do(t, "POST", "/api/v1/projects/p-patch-ok/repos", cookie, map[string]any{
		"name":                "bitnami",
		"type":                "helm",
		"is_mirror":           true,
		"mirror_upstream_url": "oci://registry-1.docker.io/bitnamicharts/nginx",
		"mirror_cred_id":      credID,
	}); resp.StatusCode != 200 {
		t.Fatalf("create: %d %+v", resp.StatusCode, body)
	}

	resp, body := s.do(t, "PATCH", "/api/v1/projects/p-patch-ok/repos/helm/bitnami", cookie, map[string]any{
		"scan_on_sync": true,
	})
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200; body=%+v", resp.StatusCode, body)
	}
}

// Compile-time checks: ensure we reference the api package's test
// harness types the same way other mirror tests do; errors_bridge_test
// already validates envelope shape — this set of tests only asserts on
// code strings.
var _ = api.CreateRepoRequest{}
var _ = strings.Contains
