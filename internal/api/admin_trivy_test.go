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
