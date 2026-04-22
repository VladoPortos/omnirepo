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

// TestRepoContent_DockerIndexTag_AggregatesChildrenScans is the F-05.3
// regression. A tag pointing at an OCI image index previously showed
// "Not scanned" forever because the /content query LEFT JOINs scan rows
// by artifact_id = tag digest — and image indexes are intentionally not
// scanned (see scan/manifest_classify.go:52). The fix aggregates the
// latest scan of each referenced child manifest and synthesises a
// status + severity-count rollup the UI treats exactly like a direct
// scan row.
func TestRepoContent_DockerIndexTag_AggregatesChildrenScans(t *testing.T) {
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
	cookie, _, _ := s.login(t, "root", pw)

	ctx := context.Background()
	pid, _ := s.deps.Projects.Create(ctx, "idxproj", "")
	repoID, _ := s.deps.Repos.Create(ctx, pid, "docker", "idx", "", nil, nil, nil)

	// Seed two child image manifests with one known scan each.
	indexBody := []byte(`{"manifests":[{"digest":"sha256:child-amd64"},{"digest":"sha256:child-arm64"}]}`)
	if _, err := s.db.Writer.ExecContext(ctx,
		`INSERT INTO docker_manifests(digest, repo_id, media_type, body, size_bytes, ref_count) VALUES
		 ('sha256:child-amd64', ?, 'application/vnd.oci.image.manifest.v1+json', X'7b7d', 100, 1),
		 ('sha256:child-arm64', ?, 'application/vnd.oci.image.manifest.v1+json', X'7b7d', 100, 1),
		 ('sha256:index',       ?, 'application/vnd.oci.image.index.v1+json',    ?,       200, 0)`,
		repoID, repoID, repoID, indexBody,
	); err != nil {
		t.Fatalf("seed manifests: %v", err)
	}
	if _, err := s.db.Writer.ExecContext(ctx,
		`INSERT INTO docker_tags(repo_id, image, tag, digest) VALUES (?, '', 'latest', 'sha256:index')`,
		repoID,
	); err != nil {
		t.Fatalf("seed tag: %v", err)
	}
	// amd64 child: done, 1 high CVE.  arm64 child: done, 2 medium CVEs.
	// Aggregate rollup must therefore show status=done, high=1, medium=2.
	if _, err := s.db.Writer.ExecContext(ctx,
		`INSERT INTO scans(repo_id, artifact_kind, artifact_id, status, severity_summary_json, finished_at) VALUES
		 (?, 'docker', 'sha256:child-amd64', 'done', '{"critical":0,"high":1,"medium":0,"low":0}', CURRENT_TIMESTAMP),
		 (?, 'docker', 'sha256:child-arm64', 'done', '{"critical":0,"high":0,"medium":2,"low":0}', CURRENT_TIMESTAMP)`,
		repoID, repoID,
	); err != nil {
		t.Fatalf("seed scans: %v", err)
	}

	resp, raw := s.doRaw(t, "GET", "/api/v1/projects/idxproj/repos/docker/idx/content", cookie, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.StatusCode, raw)
	}
	var page struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatalf("decode: %v body=%s", err, raw)
	}
	if len(page.Items) != 1 {
		t.Fatalf("want 1 row, got %d; body=%s", len(page.Items), raw)
	}
	row := page.Items[0]
	if got := row["scan_severity"]; got != "high" {
		t.Errorf("scan_severity=%v, want 'high' (worst across children)", got)
	}
	extra, _ := row["extra"].(map[string]any)
	if got := extra["scan_status"]; got != "done" {
		t.Errorf("extra.scan_status=%v, want 'done'", got)
	}
	counts, _ := extra["severity_counts"].(map[string]any)
	if counts["high"].(float64) != 1 || counts["medium"].(float64) != 2 {
		t.Errorf("severity_counts=%v, want high=1 medium=2", counts)
	}
}

// TestRepoContent_DockerIndexTag_ScanningWinsOverDone pins the status
// precedence: any child pending/running → status='scanning' (→ UI
// 'scanning' label) even if another child is already done. Otherwise the
// user would see "Clean" prematurely while other arches are still being
// scanned.
func TestRepoContent_DockerIndexTag_ScanningWinsOverDone(t *testing.T) {
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
	cookie, _, _ := s.login(t, "root", pw)

	ctx := context.Background()
	pid, _ := s.deps.Projects.Create(ctx, "idxproj2", "")
	repoID, _ := s.deps.Repos.Create(ctx, pid, "docker", "idx", "", nil, nil, nil)

	indexBody := []byte(`{"manifests":[{"digest":"sha256:child-a"},{"digest":"sha256:child-b"}]}`)
	if _, err := s.db.Writer.ExecContext(ctx,
		`INSERT INTO docker_manifests(digest, repo_id, media_type, body, size_bytes, ref_count) VALUES
		 ('sha256:child-a', ?, 'application/vnd.oci.image.manifest.v1+json', X'7b7d', 100, 1),
		 ('sha256:child-b', ?, 'application/vnd.oci.image.manifest.v1+json', X'7b7d', 100, 1),
		 ('sha256:index2',  ?, 'application/vnd.docker.distribution.manifest.list.v2+json', ?, 200, 0)`,
		repoID, repoID, repoID, indexBody,
	); err != nil {
		t.Fatalf("seed manifests: %v", err)
	}
	if _, err := s.db.Writer.ExecContext(ctx,
		`INSERT INTO docker_tags(repo_id, image, tag, digest) VALUES (?, '', 'latest', 'sha256:index2')`,
		repoID,
	); err != nil {
		t.Fatalf("seed tag: %v", err)
	}
	if _, err := s.db.Writer.ExecContext(ctx,
		`INSERT INTO scans(repo_id, artifact_kind, artifact_id, status, severity_summary_json) VALUES
		 (?, 'docker', 'sha256:child-a', 'done',    '{"critical":0,"high":0,"medium":0,"low":0}'),
		 (?, 'docker', 'sha256:child-b', 'running', '{}')`,
		repoID, repoID,
	); err != nil {
		t.Fatalf("seed scans: %v", err)
	}

	resp, raw := s.doRaw(t, "GET", "/api/v1/projects/idxproj2/repos/docker/idx/content", cookie, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.StatusCode, raw)
	}
	var page struct{ Items []map[string]any `json:"items"` }
	_ = json.Unmarshal(raw, &page)
	extra, _ := page.Items[0]["extra"].(map[string]any)
	if got := extra["scan_status"]; got != "pending" {
		t.Errorf("scan_status=%v, want 'pending' while a child is still running", got)
	}
	if got := page.Items[0]["scan_severity"]; got != "scanning" {
		t.Errorf("scan_severity=%v, want 'scanning'", got)
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

// TestRepoContent_DockerSizeSumsLayers verifies that the size_bytes
// returned for a docker tag row sums the manifest body + the referenced
// config blob + all referenced layer blobs, rather than reporting just
// the manifest body size (typically a few KB). Operators inspecting a
// populated repo need to see "this image is 75 MB" not "this manifest is
// 3 KB".
func TestRepoContent_DockerSizeSumsLayers(t *testing.T) {
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
	cookie, _, code := s.login(t, "root", pw)
	if code != 200 {
		t.Fatalf("login code=%d", code)
	}

	ctx := context.Background()
	pid, _ := s.deps.Projects.Create(ctx, "proj", "")
	repoID, _ := s.deps.Repos.Create(ctx, pid, "docker", "hub", "", nil, nil, nil)

	// Manifest body references cfg-blob (1_000_000 B) + 2 layers
	// (5_000_000 B + 7_500_000 B). Manifest blob itself weighs 500 B.
	// Expected total = 500 + 1_000_000 + 5_000_000 + 7_500_000 = 13_500_500.
	manifestBody := []byte(`{
		"schemaVersion": 2,
		"config": {"digest": "sha256:cfg", "size": 1000000},
		"layers": [
			{"digest": "sha256:l1", "size": 5000000},
			{"digest": "sha256:l2", "size": 7500000}
		]
	}`)
	_, err := s.db.Writer.ExecContext(ctx,
		`INSERT INTO docker_manifests(digest, repo_id, media_type, body, size_bytes, ref_count)
		 VALUES (?, ?, 'application/vnd.oci.image.manifest.v1+json', ?, 500, 1)`,
		"sha256:m1", repoID, manifestBody)
	if err != nil {
		t.Fatalf("seed manifest: %v", err)
	}
	for _, row := range []struct {
		digest string
		size   int64
	}{
		{"sha256:cfg", 1_000_000},
		{"sha256:l1", 5_000_000},
		{"sha256:l2", 7_500_000},
	} {
		if _, err := s.db.Writer.ExecContext(ctx,
			`INSERT INTO docker_blobs(digest, size_bytes, ref_count) VALUES (?, ?, 1)`,
			row.digest, row.size); err != nil {
			t.Fatalf("seed blob %s: %v", row.digest, err)
		}
	}
	if _, err := s.db.Writer.ExecContext(ctx,
		`INSERT INTO docker_tags(repo_id, image, tag, digest) VALUES (?, 'nginx', '1.27', ?)`,
		repoID, "sha256:m1"); err != nil {
		t.Fatalf("seed tag: %v", err)
	}

	resp, raw := s.doRaw(t, "GET", "/api/v1/projects/proj/repos/docker/hub/content", cookie, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.StatusCode, raw)
	}
	var page struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatalf("decode: %v body=%s", err, raw)
	}
	if len(page.Items) != 1 {
		t.Fatalf("got %d items, want 1; body=%s", len(page.Items), raw)
	}
	got := int64(page.Items[0]["size_bytes"].(float64))
	want := int64(500 + 1_000_000 + 5_000_000 + 7_500_000)
	if got != want {
		t.Errorf("size_bytes=%d, want %d (manifest+config+layers)", got, want)
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
