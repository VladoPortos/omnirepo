package api_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vladoportos/omnirepo/internal/api"
	"github.com/vladoportos/omnirepo/internal/auth"
)

func TestAdminTrivy_DBStatus_NoDB(t *testing.T) {
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
	cookie, _, _ := s.login(t, "root", pw)

	resp, body := s.do(t, "GET", "/api/v1/admin/trivy/db/status", cookie, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("code=%d body=%v", resp.StatusCode, body)
	}
	// No trivy_db_meta rows and no files on disk → source=none.
	if body["source"] != "none" {
		t.Fatalf("expected source=none, got %v", body["source"])
	}
}

func TestAdminTrivy_DBStatus_BakedIn(t *testing.T) {
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
	cookie, _, _ := s.login(t, "root", pw)

	// Create trivy/db directory with a dummy file to simulate baked-in DB.
	dbDir := filepath.Join(s.dataRoot, "trivy", "db")
	if err := os.MkdirAll(dbDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dbDir, "metadata.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	resp, body := s.do(t, "GET", "/api/v1/admin/trivy/db/status", cookie, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("code=%d", resp.StatusCode)
	}
	if body["source"] != "baked-in" {
		t.Fatalf("expected source=baked-in, got %v", body["source"])
	}
}

func TestAdminTrivy_DBUpload(t *testing.T) {
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
	cookie, _, _ := s.login(t, "root", pw)

	// Real Trivy release tarballs ship trivy.db + metadata.json at the
	// root and the DB file is ≥ 100 MiB in practice. Pack a ≥ 1 MiB
	// payload at the canonical root path so the validator accepts
	// the upload.
	tarBuf := createFakeTarGz(t, map[string]string{
		"metadata.json": `{"Version": 2, "UpdatedAt": "2026-04-22T00:00:00Z"}`,
		"trivy.db":      fakeTrivyDBBytes(2 << 20), // 2 MiB
	})

	// Upload via multipart.
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, _ := w.CreateFormFile("db", "trivy-db.tar.gz")
	_, _ = part.Write(tarBuf)
	_ = w.Close()

	req, _ := http.NewRequest("POST", s.ts.URL+"/api/v1/admin/trivy/db", &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: cookie})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("upload code=%d", resp.StatusCode)
	}

	// Verify trivy_db_meta row was inserted with the metadata-derived
	// version, not the upload filename.
	var source, version string
	err = s.db.Reader.QueryRowContext(context.Background(),
		`SELECT source, version FROM trivy_db_meta ORDER BY id DESC LIMIT 1`).Scan(&source, &version)
	if err != nil {
		t.Fatal(err)
	}
	if source != "uploaded" {
		t.Fatalf("source=%q want uploaded", source)
	}
	if !strings.HasPrefix(version, "schema=2") {
		t.Fatalf("version=%q want schema=2 prefix from metadata.json", version)
	}

	// Verify files exist on disk.
	dbDir := filepath.Join(s.dataRoot, "trivy", "db")
	if _, err := os.Stat(dbDir); err != nil {
		t.Fatalf("DB dir missing after upload: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dbDir, "trivy.db")); err != nil {
		t.Fatalf("trivy.db missing at root after swap: %v", err)
	}
}

// Uploading a valid .tar.gz that doesn't contain a Trivy DB
// (anyone's random backup archive) used to SwapDir-wipe the live scanner.
// The handler must reject the upload and leave any existing DB dir
// untouched.
func TestAdminTrivy_DBUpload_RejectsNonTrivyArchive(t *testing.T) {
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
	cookie, _, _ := s.login(t, "root", pw)

	dbDir := filepath.Join(s.dataRoot, "trivy", "db")
	if err := os.MkdirAll(dbDir, 0o750); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(dbDir, "existing-db-sentinel")
	if err := os.WriteFile(sentinel, []byte("live-db-marker"), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name  string
		files map[string]string
	}{
		{"no trivy.db at root", map[string]string{
			"metadata.json": `{"Version": 2}`,
			"db/trivy.db":   fakeTrivyDBBytes(2 << 20),
		}},
		{"trivy.db too small", map[string]string{
			"metadata.json": `{"Version": 2}`,
			"trivy.db":      "tiny",
		}},
		{"empty archive", map[string]string{}},
		{"only metadata.json", map[string]string{
			"metadata.json": `{"Version": 2}`,
		}},
		{"bad metadata.json", map[string]string{
			"metadata.json": `not valid json`,
			"trivy.db":      fakeTrivyDBBytes(2 << 20),
		}},
		// A 2 MiB non-BoltDB file passes the size
		// floor + filename check. Without the magic-byte sniff it would
		// be swapped in and later blow up inside the scanner. Fake the
		// magic-bytes-stripped body so only verifyBoltDBMagic rejects it.
		{"no BoltDB magic", map[string]string{
			"metadata.json": `{"Version": 2}`,
			"trivy.db":      strings.Repeat("a", 2<<20),
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tarBuf := createFakeTarGz(t, tc.files)

			var body bytes.Buffer
			w := multipart.NewWriter(&body)
			part, _ := w.CreateFormFile("db", "not-really-a-trivy-db.tar.gz")
			_, _ = part.Write(tarBuf)
			_ = w.Close()

			req, _ := http.NewRequest("POST", s.ts.URL+"/api/v1/admin/trivy/db", &body)
			req.Header.Set("Content-Type", w.FormDataContentType())
			req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: cookie})
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusUnprocessableEntity {
				t.Fatalf("expected 422, got %d", resp.StatusCode)
			}

			// Live DB untouched — sentinel file survives every rejected
			// upload attempt.
			if _, err := os.Stat(sentinel); err != nil {
				t.Fatalf("live DB sentinel was removed despite rejected upload (%s): %v", tc.name, err)
			}
		})
	}
}

func TestAdminTrivy_DBUpload_PathTraversal(t *testing.T) {
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
	cookie, _, _ := s.login(t, "root", pw)

	// Create a tar.gz with a path traversal entry.
	tarBuf := createTarGzWithTraversal(t)

	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, _ := w.CreateFormFile("db", "evil.tar.gz")
	_, _ = part.Write(tarBuf)
	_ = w.Close()

	req, _ := http.NewRequest("POST", s.ts.URL+"/api/v1/admin/trivy/db", &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: cookie})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for path traversal, got %d", resp.StatusCode)
	}
	// The shape validator runs after the
	// traversal guard. Without this body check a regression in the
	// traversal guard could be silently masked by the later
	// "missing trivy.db" validator returning the same 422. Assert the
	// specific traversal-guard message to keep that path exercised.
	bodyBytes, _ := io.ReadAll(resp.Body)
	bodyStr := string(bodyBytes)
	if !strings.Contains(bodyStr, "path traversal") && !strings.Contains(bodyStr, "path escape") {
		t.Fatalf("422 did not come from the traversal guard: body=%s", bodyStr)
	}
}

// TestAdminTrivy_DBHistory_Empty — /admin/trivy/db/history used to 404
// because the route was never mounted, which silently left the UI's
// "Update History" table blank. Guard the route and shape of the
// response so a future regression doesn't re-introduce the empty-table
// bug.
func TestAdminTrivy_DBHistory_Empty(t *testing.T) {
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
	cookie, _, _ := s.login(t, "root", pw)

	resp, body := s.do(t, "GET", "/api/v1/admin/trivy/db/history", cookie, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("code=%d body=%v", resp.StatusCode, body)
	}
	items, ok := body["items"].([]any)
	if !ok {
		t.Fatalf("items not an array: %T = %v", body["items"], body["items"])
	}
	if len(items) != 0 {
		t.Fatalf("expected empty items, got %v", items)
	}
}

// TestAdminTrivy_DBHistory_AfterUpload — after an upload inserts a
// trivy_db_meta row, the history endpoint returns it. Covers the
// upload→history round-trip the UI depends on.
func TestAdminTrivy_DBHistory_AfterUpload(t *testing.T) {
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
	cookie, _, _ := s.login(t, "root", pw)

	tarBuf := createFakeTarGz(t, map[string]string{
		"metadata.json": `{"Version": 2, "UpdatedAt": "2026-04-22T00:00:00Z"}`,
		"trivy.db":      fakeTrivyDBBytes(2 << 20),
	})
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, _ := w.CreateFormFile("db", "trivy-db.tar.gz")
	_, _ = part.Write(tarBuf)
	_ = w.Close()
	req, _ := http.NewRequest("POST", s.ts.URL+"/api/v1/admin/trivy/db", &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: cookie})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("upload code=%d", resp.StatusCode)
	}

	resp2, hbody := s.do(t, "GET", "/api/v1/admin/trivy/db/history", cookie, nil)
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("history code=%d", resp2.StatusCode)
	}
	items, _ := hbody["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("expected 1 history row, got %v", items)
	}
	row := items[0].(map[string]any)
	if row["source"] != "uploaded" {
		t.Fatalf("row source=%v want uploaded", row["source"])
	}
	if _, ok := row["size_bytes"].(float64); !ok {
		t.Fatalf("row size_bytes missing/wrong type: %T", row["size_bytes"])
	}
}

// TestAdminTrivy_PullStatus_Idle — pull-status returns a well-formed
// idle envelope before any pull has been triggered. The UI hook polls
// this while the page is open, so the always-on-200 contract matters.
func TestAdminTrivy_PullStatus_Idle(t *testing.T) {
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
	cookie, _, _ := s.login(t, "root", pw)

	resp, body := s.do(t, "GET", "/api/v1/admin/trivy/db/pull/status", cookie, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("code=%d body=%v", resp.StatusCode, body)
	}
	// state must be present and one of the known values.
	state, _ := body["state"].(string)
	switch state {
	case "idle", "running", "success", "failure":
	default:
		t.Fatalf("unexpected state=%q", state)
	}
	// bytes_downloaded is always present, even at idle.
	if _, ok := body["bytes_downloaded"]; !ok {
		t.Fatalf("bytes_downloaded missing: %v", body)
	}
}

// TestAdminTrivy_DBStatus_IncludesPath — the status response must
// include `path` so the UI can display the on-disk location. Regression
// guard against an earlier response that omitted it.
func TestAdminTrivy_DBStatus_IncludesPath(t *testing.T) {
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
	cookie, _, _ := s.login(t, "root", pw)

	resp, body := s.do(t, "GET", "/api/v1/admin/trivy/db/status", cookie, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("code=%d", resp.StatusCode)
	}
	path, ok := body["path"].(string)
	if !ok || path == "" {
		t.Fatalf("path missing/empty: %v", body["path"])
	}
	// size_bytes is always present (0 for source=none).
	if _, ok := body["size_bytes"]; !ok {
		t.Fatalf("size_bytes missing: %v", body)
	}
}

func TestAdminTrivy_NonSuperAdmin403(t *testing.T) {
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "alice", "a@x", false, false)
	cookie, _, _ := s.login(t, "alice", pw)

	resp, _ := s.do(t, "GET", "/api/v1/admin/trivy/db/status", cookie, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("code=%d want 403", resp.StatusCode)
	}
}

// fakeTrivyDBBytes returns a byte slice that passes the validator
// — size ≥ minTrivyDBBytes AND bytes[16:20] == BoltDB magic. The rest is
// zero-fill; the scanner itself is never invoked in these tests so we
// don't need a full BoltDB file.
func fakeTrivyDBBytes(size int) string {
	if size < 20 {
		size = 20
	}
	b := make([]byte, size)
	// offset 16: little-endian BoltDB magic {0xED, 0xDA, 0x0C, 0xED}
	b[16] = 0xed
	b[17] = 0xda
	b[18] = 0x0c
	b[19] = 0xed
	return string(b)
}

// createFakeTarGz builds a tar.gz in memory with the given file contents.
func createFakeTarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	for name, content := range files {
		hdr := &tar.Header{
			Name: name,
			Mode: 0o644,
			Size: int64(len(content)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestAdminTrivy_HonorsCustomDBDir pins the behaviour: when
// Deps.TrivyDBDir is set (sourced from cfg.Trivy.DBPath), the admin handlers
// must use it instead of the hardcoded DataRoot/trivy/db, so operator config
// overrides don't diverge from what the scan runner reads.
func TestAdminTrivy_HonorsCustomDBDir(t *testing.T) {
	customRoot := t.TempDir()
	customDB := filepath.Join(customRoot, "trivy-elsewhere", "db")

	s := newTestServer(t, func(d *api.Deps) {
		d.TrivyDBDir = customDB
	})
	_, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
	cookie, _, _ := s.login(t, "root", pw)

	// Seed the custom dir with a file so status reports baked-in.
	if err := os.MkdirAll(customDB, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(customDB, "metadata.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	resp, body := s.do(t, "GET", "/api/v1/admin/trivy/db/status", cookie, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status code=%d", resp.StatusCode)
	}
	if body["source"] != "baked-in" {
		t.Fatalf("expected source=baked-in from custom dir, got %v", body["source"])
	}

	// Confirm default DataRoot/trivy/db is NOT where the file lived — it has
	// no trivy dir at all, proving admin_trivy actually read from the custom
	// path rather than the legacy hardcoded fallback.
	defaultDB := filepath.Join(s.dataRoot, "trivy", "db")
	if _, err := os.Stat(defaultDB); !os.IsNotExist(err) {
		t.Fatalf("legacy default dir was accessed: stat err=%v", err)
	}

	// Upload: the new DB should land at customDB, not the legacy default.
	// Needs a realistic tarball shape so the validator accepts it.
	tarBuf := createFakeTarGz(t, map[string]string{
		"metadata.json": `{"Version": 2}`,
		"trivy.db":      fakeTrivyDBBytes(2 << 20),
		"new-marker":    "v2",
	})
	var upBody bytes.Buffer
	w := multipart.NewWriter(&upBody)
	part, _ := w.CreateFormFile("db", "trivy-db.tar.gz")
	_, _ = part.Write(tarBuf)
	_ = w.Close()
	req, _ := http.NewRequest("POST", s.ts.URL+"/api/v1/admin/trivy/db", &upBody)
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: cookie})
	upResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = upResp.Body.Close() }()
	if upResp.StatusCode != http.StatusOK {
		t.Fatalf("upload code=%d", upResp.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(customDB, "new-marker")); err != nil {
		t.Fatalf("new DB did not land at customDB: %v", err)
	}
	if _, err := os.Stat(defaultDB); !os.IsNotExist(err) {
		t.Fatalf("upload wrote to legacy default path: err=%v", err)
	}
}

// createTarGzWithTraversal builds a tar.gz with a "../" path traversal entry.
func createTarGzWithTraversal(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	hdr := &tar.Header{
		Name: "../../../etc/evil",
		Mode: 0o644,
		Size: 4,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("evil")); err != nil {
		t.Fatal(err)
	}
	_ = tw.Close()
	_ = gw.Close()
	return buf.Bytes()
}

// TestAdminTrivy_DBPull_UsesConfiguredBinaryPath — regression guard.
// Before this change, runTrivyDBPull invoked
// exec.CommandContext(ctx, "trivy", ...), bypassing cfg.Trivy.BinaryPath
// even though the scan runner (internal/scan/trivy.go) honored it. That
// drift meant operators with a non-PATH trivy install (e.g. /opt/trivy
// or a versioned /usr/local/lib/omnirepo/trivy-0.69.3) saw scans run
// against the configured binary while the admin "Pull DB" button
// shelled out to whatever happened to be on $PATH — including, in the
// air-gapped target environment, no trivy at all → bewildering
// "Unable to reach the Trivy database server" errors.
//
// The fix plumbs cfg.Trivy.BinaryPath into Deps.TrivyBinary, surfaced
// via Deps.trivyBinary(). This test:
//
//  1. drops a fake "trivy" shell script into a tempdir, makes it
//     executable, and points Deps.TrivyBinary at it
//  2. fires POST /api/v1/admin/trivy/db/pull
//  3. waits for the background goroutine to finish (state != running)
//  4. asserts the script's marker file exists — which is only true if
//     exec.CommandContext resolved Deps.TrivyBinary
//
// If a future refactor reverts to the hardcoded literal, exec resolves
// "trivy" via $PATH and runs whatever real trivy is installed on the
// dev machine (or fails with ENOENT in CI where trivy isn't on PATH);
// either way the fake script is never invoked, the marker file never
// appears, and this test fails with a precise message pointing at the
// regression.
func TestAdminTrivy_DBPull_UsesConfiguredBinaryPath(t *testing.T) {
	binDir := t.TempDir()
	markerDir := t.TempDir()
	markerFile := filepath.Join(markerDir, "trivy-was-invoked")
	scriptPath := filepath.Join(binDir, "fake-trivy")

	// The fake binary writes a marker file (proving it was invoked)
	// and creates the cache-dir/db/ layout runTrivyDBPull expects so
	// the subsequent SwapDir succeeds and pullJob ends in "success".
	// Args from runTrivyDBPull: image --download-db-only --cache-dir <tmp>.
	//
	// Note: the script intentionally writes a small marker file inside
	// db/ so storage.SwapDir has a non-empty source directory to move.
	script := "#!/bin/sh\n" +
		"set -e\n" +
		"echo \"$@\" > " + filepath.Join(markerDir, "trivy-args") + "\n" +
		"touch " + markerFile + "\n" +
		// $4 is the value passed for --cache-dir (positional in our argv).
		"mkdir -p \"$4/db\"\n" +
		"echo '{}' > \"$4/db/metadata.json\"\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}

	s := newTestServer(t, func(d *api.Deps) {
		d.TrivyBinary = scriptPath
	})
	_, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
	cookie, _, _ := s.login(t, "root", pw)

	// Kick the pull. The endpoint returns 202 immediately and runs the
	// goroutine in the background.
	resp, body := s.do(t, "POST", "/api/v1/admin/trivy/db/pull", cookie, nil)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("pull start: code=%d body=%v", resp.StatusCode, body)
	}

	// Poll the status endpoint until the job leaves the "running"
	// state. 10s window with 50ms tick is plenty for a shell script
	// that does five `touch` calls.
	deadline := time.Now().Add(10 * time.Second)
	var finalState string
	for time.Now().Before(deadline) {
		statusResp, statusBody := s.do(t, "GET", "/api/v1/admin/trivy/db/pull/status", cookie, nil)
		if statusResp.StatusCode != http.StatusOK {
			t.Fatalf("pull status: code=%d", statusResp.StatusCode)
		}
		if state, _ := statusBody["state"].(string); state != "running" {
			finalState = state
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if finalState == "" {
		t.Fatalf("pull job never left state=running within 10s")
	}

	// The core assertion: the configured binary was invoked. If
	// admin_trivy regresses to exec.CommandContext(ctx, "trivy", ...),
	// the fake script is never run and this file never appears.
	if _, err := os.Stat(markerFile); err != nil {
		t.Fatalf("configured binary path not used — marker file missing at %s (err=%v); "+
			"runTrivyDBPull likely regressed to the hardcoded \"trivy\" literal", markerFile, err)
	}

	// Belt-and-braces: the args file confirms runTrivyDBPull passed
	// the documented argv shape (image --download-db-only --cache-dir <dir>).
	argsBytes, err := os.ReadFile(filepath.Join(markerDir, "trivy-args"))
	if err != nil {
		t.Fatalf("read args marker: %v", err)
	}
	args := strings.TrimSpace(string(argsBytes))
	if !strings.HasPrefix(args, "image --download-db-only --cache-dir ") {
		t.Fatalf("unexpected argv passed to fake trivy: %q", args)
	}

	// And the pull should have completed cleanly: the fake script
	// produced a valid db/ layout, SwapDir succeeded, and the worker
	// flipped to "success". A "failure" here would indicate the binary
	// was invoked but the rest of runTrivyDBPull tripped — still proves
	// the fix, but worth surfacing for diagnosis.
	if finalState != "success" {
		t.Logf("pull final state=%q (binary invocation verified, but DB install path failed)", finalState)
	}
}
