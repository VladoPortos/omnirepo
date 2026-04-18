package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

// TestRepoContent_DockerListsTags covers F-T10: the content endpoint used to
// early-return [] for docker repos, so the UI showed "No artifacts yet"
// even when docker_tags/docker_manifests had rows. Now it joins tags+
// manifests and returns one entry per (image, tag) pair.
func TestRepoContent_DockerListsTags(t *testing.T) {
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
	cookie, _, code := s.login(t, "root", pw)
	if code != 200 {
		t.Fatalf("login code=%d", code)
	}

	ctx := context.Background()
	pid, _ := s.deps.Projects.Create(ctx, "proj", "")
	repoID, _ := s.deps.Repos.Create(ctx, pid, "docker", "hub", "", nil, nil, nil)

	_, err := s.db.Writer.ExecContext(ctx,
		`INSERT INTO docker_manifests(digest, repo_id, media_type, body, size_bytes, ref_count)
		 VALUES (?, ?, 'application/vnd.oci.image.manifest.v1+json', X'7b7d', 4096, 1)`,
		"sha256:abc", repoID)
	if err != nil {
		t.Fatalf("seed manifest: %v", err)
	}
	_, err = s.db.Writer.ExecContext(ctx,
		`INSERT INTO docker_tags(repo_id, image, tag, digest) VALUES (?, 'alpine', '3.20', ?)`,
		repoID, "sha256:abc")
	if err != nil {
		t.Fatalf("seed tag: %v", err)
	}
	// A legacy pre-migration-021 tag with empty image, same digest.
	_, err = s.db.Writer.ExecContext(ctx,
		`INSERT INTO docker_tags(repo_id, image, tag, digest) VALUES (?, '', 'latest', ?)`,
		repoID, "sha256:abc")
	if err != nil {
		t.Fatalf("seed legacy tag: %v", err)
	}

	resp, raw := s.doRaw(t, "GET", "/api/v1/projects/proj/repos/docker/hub/content", cookie, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.StatusCode, raw)
	}
	var entries []map[string]any
	if err := json.Unmarshal(raw, &entries); err != nil {
		t.Fatalf("decode: %v body=%s", err, raw)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2; body=%s", len(entries), raw)
	}
	// Ordering is (image ASC, tag ASC): '' comes before 'alpine'.
	if entries[0]["name"] != "latest" {
		t.Errorf("entries[0].name=%v, want latest", entries[0]["name"])
	}
	if entries[1]["name"] != "alpine:3.20" {
		t.Errorf("entries[1].name=%v, want alpine:3.20", entries[1]["name"])
	}
	extra, _ := entries[1]["extra"].(map[string]any)
	if extra["digest"] != "sha256:abc" {
		t.Errorf("extra.digest=%v", extra["digest"])
	}
	if extra["image"] != "alpine" {
		t.Errorf("extra.image=%v", extra["image"])
	}
}

// TestRepoContent_DockerEmptyRepoReturnsEmptyArray keeps the contract for
// brand-new docker repos: the endpoint must return [] (not null) so the UI
// renders the empty-state card instead of treating the response as "error".
func TestRepoContent_DockerEmptyRepoReturnsEmptyArray(t *testing.T) {
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
	cookie, _, code := s.login(t, "root", pw)
	if code != 200 {
		t.Fatalf("login code=%d", code)
	}
	ctx := context.Background()
	pid, _ := s.deps.Projects.Create(ctx, "proj", "")
	_, _ = s.deps.Repos.Create(ctx, pid, "docker", "empty", "", nil, nil, nil)

	resp, raw := s.doRaw(t, "GET", "/api/v1/projects/proj/repos/docker/empty/content", cookie, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.StatusCode, raw)
	}
	// Handler returns a JSON array; empty list must serialise as [].
	if string(raw) != "[]\n" && string(raw) != "[]" {
		t.Errorf("expected `[]`, got %q", string(raw))
	}
}
