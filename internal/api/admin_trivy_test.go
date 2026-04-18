package api_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/dxc-internal/omnirepo/internal/api"
	"github.com/dxc-internal/omnirepo/internal/auth"
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

	// Create a fake tar.gz with a single file.
	tarBuf := createFakeTarGz(t, map[string]string{
		"metadata.json": `{"version": 2}`,
		"db/trivy.db":   "fake-db-content",
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

	// Verify trivy_db_meta row was inserted.
	var source string
	err = s.db.Reader.QueryRowContext(context.Background(),
		`SELECT source FROM trivy_db_meta ORDER BY id DESC LIMIT 1`).Scan(&source)
	if err != nil {
		t.Fatal(err)
	}
	if source != "uploaded" {
		t.Fatalf("source=%q want uploaded", source)
	}

	// Verify files exist on disk.
	dbDir := filepath.Join(s.dataRoot, "trivy", "db")
	if _, err := os.Stat(dbDir); err != nil {
		t.Fatalf("DB dir missing after upload: %v", err)
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
		"metadata.json": `{"version": 2}`,
		"db/trivy.db":   "fake-db-content",
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
// guard against the pre-Phase-7 response that omitted it.
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

// TestAdminTrivy_HonorsCustomDBDir pins audit finding #4: when
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
	tarBuf := createFakeTarGz(t, map[string]string{
		"new-marker": "v2",
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
