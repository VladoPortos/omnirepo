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
	var page struct {
		Items      []map[string]any `json:"items"`
		Total      int64            `json:"total"`
		NextOffset *int64           `json:"next_offset"`
	}
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatalf("decode: %v body=%s", err, raw)
	}
	if len(page.Items) != 2 {
		t.Fatalf("got %d items, want 2; body=%s", len(page.Items), raw)
	}
	if page.Total != 2 {
		t.Errorf("total=%d, want 2", page.Total)
	}
	if page.NextOffset != nil {
		t.Errorf("next_offset=%v, want nil (final page)", *page.NextOffset)
	}
	// Ordering is (image ASC, tag ASC): '' comes before 'alpine'.
	if page.Items[0]["name"] != "latest" {
		t.Errorf("items[0].name=%v, want latest", page.Items[0]["name"])
	}
	if page.Items[1]["name"] != "alpine:3.20" {
		t.Errorf("items[1].name=%v, want alpine:3.20", page.Items[1]["name"])
	}
	extra, _ := page.Items[1]["extra"].(map[string]any)
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
	var page struct {
		Items      []map[string]any `json:"items"`
		Total      int64            `json:"total"`
		NextOffset *int64           `json:"next_offset"`
	}
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatalf("decode: %v body=%s", err, raw)
	}
	// Empty wrapper: items=[] (NOT null — the handler initialises the slice
	// so it JSON-encodes as []), total=0, next_offset=nil.
	if page.Items == nil {
		t.Error("items is nil; want empty slice")
	}
	if len(page.Items) != 0 {
		t.Errorf("len(items)=%d, want 0", len(page.Items))
	}
	if page.Total != 0 {
		t.Errorf("total=%d, want 0", page.Total)
	}
	if page.NextOffset != nil {
		t.Errorf("next_offset=%v, want nil", *page.NextOffset)
	}
}

// TestRepoContent_PaginationNextOffset verifies limit/offset chains. Seeds
// 3 RPM rows, asks for limit=2 — first call should return items[0..1] with
// next_offset=2 and total=3; using that offset returns items[2..2] and
// next_offset=nil.
func TestRepoContent_PaginationNextOffset(t *testing.T) {
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
	cookie, _, code := s.login(t, "root", pw)
	if code != 200 {
		t.Fatalf("login code=%d", code)
	}
	ctx := context.Background()
	pid, _ := s.deps.Projects.Create(ctx, "proj", "")
	repoID, _ := s.deps.Repos.Create(ctx, pid, "rpm", "stable", "", nil, nil, nil)
	for _, name := range []string{"alpha", "beta", "gamma"} {
		_, err := s.db.Writer.ExecContext(ctx,
			`INSERT INTO rpm_packages(
				repo_id, name, version, release, arch, size_bytes, filename, digest, uploaded_at
			) VALUES (?, ?, '1.0', '1.el9', 'x86_64', 1024, ?, ?, '2026-04-18T00:00:00.000Z')`,
			repoID, name, name+"-1.0-1.el9.x86_64.rpm", "sha256:"+name+"-digest")
		if err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}

	resp, raw := s.doRaw(t, "GET",
		"/api/v1/projects/proj/repos/rpm/stable/content?limit=2", cookie, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("first status=%d body=%s", resp.StatusCode, raw)
	}
	var first struct {
		Items      []map[string]any `json:"items"`
		Total      int64            `json:"total"`
		NextOffset *int64           `json:"next_offset"`
	}
	if err := json.Unmarshal(raw, &first); err != nil {
		t.Fatalf("decode first: %v body=%s", err, raw)
	}
	if first.Total != 3 {
		t.Errorf("first.total=%d, want 3", first.Total)
	}
	if len(first.Items) != 2 {
		t.Errorf("first.items len=%d, want 2", len(first.Items))
	}
	if first.NextOffset == nil || *first.NextOffset != 2 {
		t.Fatalf("first.next_offset=%v, want 2", first.NextOffset)
	}

	resp, raw = s.doRaw(t, "GET",
		"/api/v1/projects/proj/repos/rpm/stable/content?limit=2&offset=2", cookie, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("second status=%d body=%s", resp.StatusCode, raw)
	}
	var second struct {
		Items      []map[string]any `json:"items"`
		Total      int64            `json:"total"`
		NextOffset *int64           `json:"next_offset"`
	}
	if err := json.Unmarshal(raw, &second); err != nil {
		t.Fatalf("decode second: %v body=%s", err, raw)
	}
	if second.Total != 3 {
		t.Errorf("second.total=%d, want 3", second.Total)
	}
	if len(second.Items) != 1 {
		t.Errorf("second.items len=%d, want 1", len(second.Items))
	}
	if second.NextOffset != nil {
		t.Errorf("second.next_offset=%v, want nil (reached end)", *second.NextOffset)
	}
}
