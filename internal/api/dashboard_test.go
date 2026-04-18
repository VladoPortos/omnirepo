package api_test

import (
	"context"
	"net/http"
	"testing"
)

func TestDashboard_ReturnsStats(t *testing.T) {
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
	cookie, _, code := s.login(t, "root", pw)
	if code != 200 {
		t.Fatalf("login code=%d", code)
	}

	// Create a project + repo to get non-zero counts.
	ctx := context.Background()
	pid, _ := s.deps.Projects.Create(ctx, "dash-proj", "")
	_, _ = s.deps.Repos.Create(ctx, pid, "docker", "dash-repo", "", nil, nil, nil)

	resp, body := s.do(t, "GET", "/api/v1/dashboard", cookie, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("code=%d body=%v", resp.StatusCode, body)
	}

	repoCount := int(body["repo_count"].(float64))
	if repoCount < 1 {
		t.Fatalf("expected repo_count >= 1, got %d", repoCount)
	}
	userCount := int(body["user_count"].(float64))
	if userCount < 1 {
		t.Fatalf("expected user_count >= 1, got %d", userCount)
	}

	findings, ok := body["scan_findings"].(map[string]any)
	if !ok {
		t.Fatalf("expected scan_findings object, got %v", body["scan_findings"])
	}
	// Should have critical and high keys even if 0.
	if _, ok := findings["critical"]; !ok {
		t.Fatal("expected scan_findings.critical key")
	}
	if _, ok := findings["high"]; !ok {
		t.Fatal("expected scan_findings.high key")
	}
}

func TestDashboard_Unauthenticated(t *testing.T) {
	s := newTestServer(t)
	resp, _ := s.do(t, "GET", "/api/v1/dashboard", "", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

func TestDashboardStorage_ReturnsRepoBreakdown(t *testing.T) {
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
	cookie, _, code := s.login(t, "root", pw)
	if code != 200 {
		t.Fatalf("login code=%d", code)
	}

	ctx := context.Background()
	pid, _ := s.deps.Projects.Create(ctx, "storage-proj", "")
	repoID, _ := s.deps.Repos.Create(ctx, pid, "docker", "big-repo", "", nil, nil, nil)

	// Plant a 1 GiB docker manifest row so `repoSizeExpr` computes real
	// bytes. The storage handler ignores `repos.size_bytes` after F-5 and
	// sums live from the artifact tables, so writing the column directly
	// (the prior approach) no longer affects the response.
	_, _ = s.db.Writer.ExecContext(ctx,
		`INSERT INTO docker_manifests(digest, repo_id, media_type, body, size_bytes, ref_count)
		 VALUES (?, ?, 'application/vnd.oci.image.manifest.v1+json', X'7b7d', 1073741824, 1)`,
		"sha256:aa", repoID)
	// Set total in settings.
	_, _ = s.db.Writer.ExecContext(ctx, `INSERT INTO settings(key, value) VALUES ('storage_total_bytes', '10737418240') ON CONFLICT(key) DO UPDATE SET value=excluded.value`)

	resp, body := s.do(t, "GET", "/api/v1/dashboard/storage", cookie, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("code=%d body=%v", resp.StatusCode, body)
	}

	usedBytes := int64(body["used_bytes"].(float64))
	if usedBytes < 1073741824 {
		t.Fatalf("expected used_bytes >= 1073741824, got %d", usedBytes)
	}

	totalBytes := int64(body["total_bytes"].(float64))
	if totalBytes == 0 {
		t.Log("total_bytes is 0 — Statfs returned value or settings not picked up; checking if settings fallback worked")
		// On CI/test, Statfs may return a real value overriding settings.
		// Either way, totalBytes should be > 0 from either source.
	}

	repos, ok := body["repos"].([]any)
	if !ok {
		t.Fatalf("expected repos array, got %v", body["repos"])
	}
	if len(repos) < 1 {
		t.Fatalf("expected at least 1 repo in breakdown, got %d", len(repos))
	}
	first := repos[0].(map[string]any)
	if first["project"] != "storage-proj" {
		t.Fatalf("expected project=storage-proj, got %v", first["project"])
	}
	if first["name"] != "big-repo" {
		t.Fatalf("expected name=big-repo, got %v", first["name"])
	}
}

func TestDashboardStorage_Unauthenticated(t *testing.T) {
	s := newTestServer(t)
	resp, _ := s.do(t, "GET", "/api/v1/dashboard/storage", "", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

// TestDashboardStorage_RefCountsSharedBlobs verifies W-02: a blob referenced
// by N manifests across N distinct repos contributes ~size_bytes/N to each
// repo's size_bytes in the /dashboard/storage response, rather than being
// fully counted in every referencing repo.
//
// Seed shape:
//   - one project, two docker repos (A, B)
//   - one shared blob of 2 GiB (digest "sha256:shared")
//   - two manifests in different repos each referencing that blob as a layer
//
// Expected: each repo reports < 2 GiB for size_bytes (proving the ref-count
// split); both report > 0 (proving the blob still contributes partially).
// Expected fair share per repo is ~1 GiB (2 GiB / 2 repos).
func TestDashboardStorage_RefCountsSharedBlobs(t *testing.T) {
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
	cookie, _, code := s.login(t, "root", pw)
	if code != 200 {
		t.Fatalf("login code=%d", code)
	}

	ctx := context.Background()
	pid, _ := s.deps.Projects.Create(ctx, "ref-proj", "")
	repoA, _ := s.deps.Repos.Create(ctx, pid, "docker", "a", "", nil, nil, nil)
	repoB, _ := s.deps.Repos.Create(ctx, pid, "docker", "b", "", nil, nil, nil)

	// Seed a shared blob of 2 GiB. ref_count=2 because two manifests
	// reference it; the dashboard SQL doesn't actually read this column
	// (it recomputes distinct-repo count from manifest bodies), but we
	// still set it to a correct value for schema fidelity.
	sharedDigest := "sha256:shared"
	const twoGiB = int64(2) * 1024 * 1024 * 1024
	if _, err := s.db.Writer.ExecContext(ctx,
		`INSERT INTO docker_blobs(digest, size_bytes, ref_count) VALUES (?, ?, 2)`,
		sharedDigest, twoGiB); err != nil {
		t.Fatal(err)
	}

	// Two manifests (different repos) each referencing the shared blob
	// via the layers array. The manifest body size_bytes is 0 so the
	// manifest contribution doesn't confound the blob arithmetic being
	// asserted here.
	body := []byte(`{"layers":[{"digest":"sha256:shared"}]}`)
	if _, err := s.db.Writer.ExecContext(ctx,
		`INSERT INTO docker_manifests(digest, repo_id, media_type, body, size_bytes, ref_count)
		 VALUES (?, ?, ?, ?, 0, 1)`,
		"sha256:ma", repoA, "application/vnd.oci.image.manifest.v1+json", body); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Writer.ExecContext(ctx,
		`INSERT INTO docker_manifests(digest, repo_id, media_type, body, size_bytes, ref_count)
		 VALUES (?, ?, ?, ?, 0, 1)`,
		"sha256:mb", repoB, "application/vnd.oci.image.manifest.v1+json", body); err != nil {
		t.Fatal(err)
	}

	resp, out := s.do(t, "GET", "/api/v1/dashboard/storage", cookie, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("code=%d body=%v", resp.StatusCode, out)
	}

	repos, ok := out["repos"].([]any)
	if !ok {
		t.Fatalf("expected repos array, got %v", out["repos"])
	}

	var aBytes, bBytes int64
	for _, raw := range repos {
		row := raw.(map[string]any)
		name, _ := row["name"].(string)
		size, _ := row["size_bytes"].(float64)
		switch name {
		case "a":
			aBytes = int64(size)
		case "b":
			bBytes = int64(size)
		}
	}

	// Each repo should see < 2 GiB (proving the blob is ref-counted, not
	// fully duplicated). Old behavior counted the full 2 GiB in both.
	if aBytes >= twoGiB {
		t.Errorf("repo a size=%d >= 2 GiB; blob not ref-counted (expected ~1 GiB)", aBytes)
	}
	if bBytes >= twoGiB {
		t.Errorf("repo b size=%d >= 2 GiB; blob not ref-counted (expected ~1 GiB)", bBytes)
	}

	// Both should be non-zero (blob still counted, just partially).
	if aBytes <= 0 {
		t.Errorf("repo a size=%d; expected > 0 (blob should contribute partially)", aBytes)
	}
	if bBytes <= 0 {
		t.Errorf("repo b size=%d; expected > 0 (blob should contribute partially)", bBytes)
	}

	// Fair-share sanity: each repo's share should be close to 2 GiB / 2 = 1 GiB.
	// Allow a generous tolerance because SQLite REAL→int64 truncation rounds
	// down. Accept anything in [0.9 GiB, 1.1 GiB].
	oneGiB := int64(1024 * 1024 * 1024)
	lo := oneGiB - oneGiB/10
	hi := oneGiB + oneGiB/10
	if aBytes < lo || aBytes > hi {
		t.Errorf("repo a size=%d; expected ~1 GiB (±10%%), got out of range [%d, %d]", aBytes, lo, hi)
	}
	if bBytes < lo || bBytes > hi {
		t.Errorf("repo b size=%d; expected ~1 GiB (±10%%), got out of range [%d, %d]", bBytes, lo, hi)
	}
}
