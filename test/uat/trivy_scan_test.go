//go:build uat

// Real Trivy UAT (SC-2): trivy-enforced severity gate blocks a pull of
// a known-vulnerable image.
//
// Flow:
//  1. Pre-populate a Trivy DB into a tmp cache (--download-db-only).
//  2. Boot omnirepo with cfg.Trivy.CachePath pointing at that cache.
//  3. Use the server's pull-external endpoint to pull nginx:1.14 (a
//     stable, well-documented CVE set) from Docker Hub into the local
//     registry. This exercises the same path SC-3 validates and avoids
//     the "crane push against anonymous /v2/" architectural mismatch
//     where crane skips token exchange after an anonymous-200 ping.
//  4. Trigger a rescan via POST /api/v1/.../artifacts/{digest}/rescan.
//  5. Poll GET /api/v1/scans/{id} until status='done'.
//  6. Assert severity_summary_json has >=1 CRITICAL and >=1 HIGH.
//  7. PATCH repo block_on_severity='high'; GET /v2/.../manifests/1.14
//     expects 403 with body {"error":"blocked_by_scan",...}.
package uat

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ensureTrivyDB runs `trivy image --download-db-only --cache-dir <dir>`
// so subsequent scans with --offline-scan find CVE data. Returns the
// cache dir. Skips the test if download fails.
func ensureTrivyDB(t *testing.T, trivyBin string) string {
	t.Helper()
	cache := t.TempDir()
	// DOCKER_CONFIG isolation — trivy's embedded go-containerregistry
	// reads ~/.docker/config.json which points at the broken
	// desktop.exe creds helper under Docker Desktop + WSL2.
	cleanDockerCfg := t.TempDir()
	if err := os.WriteFile(filepath.Join(cleanDockerCfg, "config.json"),
		[]byte(`{"auths":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	// Set DOCKER_CONFIG in-process so the scan handler's trivy
	// subprocess (which inherits os.Environ) also avoids the broken
	// desktop.exe helper. t.Setenv restores on test teardown.
	t.Setenv("DOCKER_CONFIG", cleanDockerCfg)
	env := append(os.Environ(),
		"TRIVY_NO_PROGRESS=1",
	)
	runDownload := func(args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		full := append([]string{}, args...)
		full = append(full, "--cache-dir", cache)
		cmd := exec.CommandContext(ctx, trivyBin, full...)
		cmd.Env = env
		var stderr strings.Builder
		cmd.Stderr = &stderr
		err := cmd.Run()
		return stderr.String(), err
	}
	// Vuln DB — the "primary" DB Trivy checks on every scan.
	if stderr, err := runDownload("image", "--download-db-only"); err != nil {
		time.Sleep(2 * time.Second)
		if stderr2, err2 := runDownload("image", "--download-db-only"); err2 != nil {
			t.Skipf("trivy --download-db-only failed twice: %v\nstderr1=%s\nstderr2=%s",
				err2, stderr, stderr2)
		}
	}
	// Java DB — Trivy refuses --skip-java-db-update on first run, so
	// seed it now. Only needed when the image under scan has Java
	// components; easier to always seed than to pick/choose.
	if stderr, err := runDownload("image", "--download-java-db-only"); err != nil {
		time.Sleep(2 * time.Second)
		if stderr2, err2 := runDownload("image", "--download-java-db-only"); err2 != nil {
			t.Skipf("trivy --download-java-db-only failed twice: %v\nstderr1=%s\nstderr2=%s",
				err2, stderr, stderr2)
		}
	}
	return cache
}

// TestTrivyScan_BlocksVulnerableImageOnPull imports nginx:1.14 via the
// pull-external REST endpoint, rescans, asserts CRITICAL+HIGH counts,
// flips the severity gate, and verifies the 403 envelope on manifest GET.
func TestTrivyScan_BlocksVulnerableImageOnPull(t *testing.T) {
	trivyBin := mustFindBin(t, "trivy")

	// Step 1: pre-populate DB.
	cache := ensureTrivyDB(t, trivyBin)

	// Step 2: boot app with shared trivy cache.
	f := bootApp(t, bootOpts{TrivyCachePath: cache})
	cookie := f.loginSession(t)
	// Disable auto-scan on the target repo so WE decide when a scan
	// runs (the deterministic rescan below is what we poll).
	autoScan := false
	f.patchRepo(t, cookie, "docker", f.repo, map[string]any{"auto_scan": autoScan})

	// Step 3: enqueue pull-external of nginx:1.14 from Docker Hub
	// (anonymous — empty cred_id, empty inline user/pass).
	//
	// We use the amd64 child-manifest digest directly rather than the
	// tag because the scan handler's oci_layout materializer does not
	// recurse through manifest lists (it treats manifest "refs" as
	// blob digests). Pinning to amd64 keeps this test linux/amd64-only
	// but removes the multi-arch dependency.
	pullURL := fmt.Sprintf("/api/v1/projects/%s/repos/docker/%s/pull-external",
		f.project, f.repo)
	pullReq := map[string]any{
		"src_image": "docker.io/library/nginx@sha256:706446e9c6667c0880d5da3f39c09a6c7d2114f5a5d6b74a2fafd24ae30d2078",
		"dst_tag":   "1.14",
	}
	resp := f.doJSON(t, "POST", pullURL, pullReq, cookie)
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("pull-external enqueue: status=%d body=%s", resp.StatusCode, body)
	}
	var pullResp struct {
		JobID int64 `json:"job_id"`
	}
	if err := json.Unmarshal(body, &pullResp); err != nil {
		t.Fatalf("decode pull-external: %v", err)
	}
	t.Logf("pull-external enqueued job_id=%d", pullResp.JobID)

	// Step 3b: poll sync_jobs.status until the pull-external job is
	// 'done'. Access via the REST scan list isn't right (different
	// table); inspect the SQLite DB directly.
	pullDeadline := time.Now().Add(6 * time.Minute)
	for time.Now().Before(pullDeadline) {
		db := openDB(t, f)
		var status, lastErr string
		err := db.QueryRow(`SELECT status, COALESCE(last_error,'') FROM sync_jobs WHERE id=?`,
			pullResp.JobID).Scan(&status, &lastErr)
		if err != nil {
			t.Fatalf("query sync_jobs: %v", err)
		}
		if status == "done" {
			break
		}
		if status == "failed" {
			// pull-external can fail once on transient upstream issue;
			// the pool auto-retries so also fail the test here.
			t.Fatalf("pull-external job failed: last_error=%s", lastErr)
		}
		time.Sleep(500 * time.Millisecond)
	}
	// Find the manifest digest that just landed.
	var manifestDigest string
	if err := openDB(t, f).QueryRow(`
		SELECT t.digest
		FROM docker_tags t
		JOIN repos r ON r.id = t.repo_id
		JOIN projects p ON p.id = r.project_id
		WHERE p.name=? AND r.name=? AND r.type='docker' AND t.tag=?
	`, f.project, f.repo, "1.14").Scan(&manifestDigest); err != nil {
		t.Fatalf("resolve manifest digest via tag: %v", err)
	}
	if !strings.HasPrefix(manifestDigest, "sha256:") {
		t.Fatalf("unexpected digest %q", manifestDigest)
	}
	t.Logf("imported nginx:1.14 digest=%s", manifestDigest)

	// Diagnostic: verify every blob the manifest references exists in
	// CAS on disk. Mismatches here == pull-external stored a different
	// digest than what the manifest says (a serious correctness bug).
	verifyCASBlobsForManifest(t, f, manifestDigest)

	// Step 4: trigger rescan via REST so we have a deterministic scan id.
	rescanURL := fmt.Sprintf("/api/v1/projects/%s/repos/docker/%s/artifacts/%s/rescan",
		f.project, f.repo, manifestDigest)
	resp = f.doJSON(t, "POST", rescanURL, nil, cookie)
	body, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("rescan: status=%d body=%s", resp.StatusCode, body)
	}
	var rescanResp struct {
		ScanID int64 `json:"scan_id"`
	}
	if err := json.Unmarshal(body, &rescanResp); err != nil {
		t.Fatalf("decode rescan body %q: %v", body, err)
	}
	scanID := rescanResp.ScanID
	t.Logf("rescan enqueued scan_id=%d", scanID)

	// Step 5: poll scan status until terminal.
	scanDeadline := time.Now().Add(8 * time.Minute)
	var finalStatus string
	var lastStatus string
	var lastAttempts int64
	var lastErr string
	var severitySummary string
	for time.Now().Before(scanDeadline) {
		resp := f.doJSON(t, "GET", fmt.Sprintf("/api/v1/scans/%d", scanID), nil, cookie)
		pb, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("get scan: status=%d body=%s", resp.StatusCode, pb)
		}
		var sr struct {
			Status              string `json:"status"`
			Attempts            int64  `json:"attempts"`
			LastError           string `json:"last_error"`
			SeveritySummaryJSON string `json:"severity_summary_json"`
		}
		if err := json.Unmarshal(pb, &sr); err != nil {
			t.Fatalf("decode scan: %v", err)
		}
		lastStatus = sr.Status
		lastAttempts = sr.Attempts
		lastErr = sr.LastError
		if sr.Status == "done" || sr.Status == "failed" {
			finalStatus = sr.Status
			severitySummary = sr.SeveritySummaryJSON
			if sr.Status == "failed" {
				t.Fatalf("scan failed: %s", sr.LastError)
			}
			break
		}
		time.Sleep(1 * time.Second)
	}
	if finalStatus != "done" {
		t.Fatalf("scan did not reach terminal status within 8min; final status=%q attempts=%d last_err=%s",
			lastStatus, lastAttempts, lastErr)
	}

	// Step 6: severity summary must contain CRITICAL + HIGH.
	if severitySummary == "" {
		t.Fatal("severity_summary_json empty on done scan")
	}
	// Diagnostic: also print the vulnerabilities-row count for this scan.
	var vulnCount int
	if err := openDB(t, f).QueryRow(
		`SELECT COUNT(*) FROM vulnerabilities WHERE scan_id=?`, scanID).Scan(&vulnCount); err != nil {
		t.Logf("vuln count query failed: %v", err)
	}
	t.Logf("scan_id=%d vulnerabilities_rows=%d severity=%s", scanID, vulnCount, severitySummary)
	// Dump all scans rows for diagnosis.
	drows, _ := openDB(t, f).Query(
		`SELECT id, status, attempts, COALESCE(severity_summary_json,'') FROM scans ORDER BY id`)
	for drows.Next() {
		var id, att int64
		var st, sev string
		if err := drows.Scan(&id, &st, &att, &sev); err == nil {
			t.Logf("  scan row: id=%d status=%s attempts=%d severity=%s", id, st, att, sev)
		}
	}
	_ = drows.Close()
	var counts map[string]int
	if err := json.Unmarshal([]byte(severitySummary), &counts); err != nil {
		t.Fatalf("decode severity summary %q: %v", severitySummary, err)
	}
	// Parser lowercases severity keys (internal/scan/parse.go).
	if counts["critical"] < 1 {
		t.Errorf("expected >=1 critical in %s", severitySummary)
	}
	if counts["high"] < 1 {
		t.Errorf("expected >=1 high in %s", severitySummary)
	}

	// Step 7: flip block_on_severity=high and verify the next manifest
	// GET returns 403 blocked_by_scan.
	f.patchRepo(t, cookie, "docker", f.repo, map[string]any{
		"block_on_severity": "high",
	})
	// The severity cache Invalidate fires at scan-done so the gate
	// should pick up immediately. Wait up to 5s for settle.
	var blockedStatus int
	var blockedBody []byte
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		req, _ := http.NewRequest("GET",
			fmt.Sprintf("%s/v2/%s/docker/%s/manifests/1.14", f.baseURL(), f.project, f.repo), nil)
		req.Header.Set("Accept", "application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json")
		tok := fetchBearerToken(t, f)
		req.Header.Set("Authorization", "Bearer "+tok)
		r, err := f.httpClient.Do(req)
		if err != nil {
			t.Fatalf("manifest GET: %v", err)
		}
		blockedStatus = r.StatusCode
		blockedBody, _ = io.ReadAll(r.Body)
		_ = r.Body.Close()
		if blockedStatus == 403 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if blockedStatus != 403 {
		t.Fatalf("expected 403 after block_on_severity=high; got status=%d body=%s",
			blockedStatus, blockedBody)
	}
	if !strings.Contains(string(blockedBody), "blocked_by_scan") {
		t.Fatalf("expected blocked_by_scan envelope; got body=%s", blockedBody)
	}
}

// fetchBearerToken does a Basic→Bearer exchange against /v2/token
// using the fixture's super-admin credentials.
// verifyCASBlobsForManifest reads the manifest body from the DB,
// parses layers+config digests, and verifies each one exists as a
// file under <DataRoot>/blobs/sha256/<xx>/<hex>. Fatal on missing.
func verifyCASBlobsForManifest(t *testing.T, f *uatFixture, manifestDigest string) {
	t.Helper()
	db := openDB(t, f)
	var body []byte
	if err := db.QueryRow(`SELECT body FROM docker_manifests WHERE digest=?`,
		manifestDigest).Scan(&body); err != nil {
		t.Fatalf("read manifest body: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	refs := []string{}
	if cfg, ok := raw["config"].(map[string]any); ok {
		if d, ok := cfg["digest"].(string); ok {
			refs = append(refs, d)
		}
	}
	if layers, ok := raw["layers"].([]any); ok {
		for _, l := range layers {
			if m, ok := l.(map[string]any); ok {
				if d, ok := m["digest"].(string); ok {
					refs = append(refs, d)
				}
			}
		}
	}
	t.Logf("manifest refs: %d blobs", len(refs))
	for _, d := range refs {
		hx := strings.TrimPrefix(d, "sha256:")
		p := filepath.Join(f.dataRoot, "blobs", "sha256", hx[:2], hx)
		st, err := os.Stat(p)
		if err != nil {
			t.Fatalf("CAS missing blob %s at %s: %v", d, p, err)
		}
		t.Logf("  blob %s: %d bytes", d, st.Size())
	}
}

func fetchBearerToken(t *testing.T, f *uatFixture) string {
	t.Helper()
	req, _ := http.NewRequest("GET", f.baseURL()+"/v2/token", nil)
	req.SetBasicAuth(f.adminLogin, f.adminPassword)
	r, err := f.httpClient.Do(req)
	if err != nil {
		t.Fatalf("token exchange: %v", err)
	}
	defer func() { _ = r.Body.Close() }()
	if r.StatusCode != 200 {
		b, _ := io.ReadAll(r.Body)
		t.Fatalf("token exchange: status=%d body=%s", r.StatusCode, b)
	}
	var tr struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&tr); err != nil {
		t.Fatalf("token decode: %v", err)
	}
	return tr.Token
}
