package api_test

// Direct unit coverage for handleDriftRestore's 409 repo-missing branch.
//
// The integration tests `SkipOnFailedSync` + `RemovesVanishedUpstreamEntries`
// already exercise this code path indirectly via the full sync engine
// pipeline. This test isolates the branch so a regression in the
// sidecar-parse → repo lookup → 409 emission flow surfaces with a
// minimal stack trace, independent of mirror sync semantics.

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vladoportos/omnirepo/internal/storage"
)

func TestHandleDriftRestore_RepoMissing_Returns409(t *testing.T) {
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
	cookie, _, _ := s.login(t, "root", pw)

	const trashID = "1714056789-pypi_file_drift-99"

	// Synthesize a drift trash entry whose row_snapshot points at a
	// repo_id that does NOT exist (no INSERT into repos table). The
	// handler's repo lookup must fall to ErrNotFound and emit 409
	// `restore.conflict.repo_missing`.
	snapshot := map[string]any{
		"repo_id":       int64(99999), // nothing seeded — guaranteed absent
		"name":          "fakepkg",
		"version":       "0.1.0",
		"file_filename": "fakepkg-0.1.0-py3-none-any.whl",
	}
	snapJSON, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	entry := storage.TrashEntry{
		Kind:         "pypi_file_drift",
		Path:         "drift/pypi_file_drift/" + trashID,
		OriginalPath: "/var/lib/omnirepo/repos/anyproj/pypi/anyrepo/files/fakepkg-0.1.0-py3-none-any.whl",
		RowSnapshot:  snapJSON,
	}

	req := httptest.NewRequest("POST", "/api/v1/admin/trash/"+trashID+"/restore", nil)
	req.Header.Set("Cookie", cookie)
	w := httptest.NewRecorder()

	s.deps.HandleDriftRestoreForTest(w, req, entry, trashID)

	if w.Code != 409 {
		t.Fatalf("status=%d, want 409 (got body: %s)", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "source repo no longer exists") {
		t.Fatalf("body missing repo-missing error code: %s", body)
	}

	// Second branch: soft-deleted repo. handleDriftRestore must treat
	// `deleted_at != NULL` as repo_missing, not "fall through to UPSERT".
	s.do(t, "POST", "/api/v1/projects", cookie, map[string]any{"name": "p1"})
	s.do(t, "POST", "/api/v1/projects/p1/repos", cookie, map[string]any{
		"name": "r1", "type": "pypi",
	})
	var repoID int64
	if err := s.db.Reader.QueryRowContext(context.Background(),
		`SELECT id FROM repos WHERE name='r1'`).Scan(&repoID); err != nil {
		t.Fatalf("scan repo id: %v", err)
	}
	if _, err := s.db.Writer.ExecContext(context.Background(),
		`UPDATE repos SET deleted_at = CURRENT_TIMESTAMP WHERE id=?`, repoID); err != nil {
		t.Fatalf("soft-delete repo: %v", err)
	}

	snapshot["repo_id"] = repoID
	snapJSON2, _ := json.Marshal(snapshot)
	entry.RowSnapshot = snapJSON2
	w2 := httptest.NewRecorder()
	s.deps.HandleDriftRestoreForTest(w2, req, entry, trashID)
	if w2.Code != 409 {
		t.Fatalf("soft-deleted-repo status=%d, want 409 (body: %s)", w2.Code, w2.Body.String())
	}
	if !strings.Contains(w2.Body.String(), "source repo no longer exists") {
		t.Fatalf("soft-deleted-repo body missing repo-missing code: %s", w2.Body.String())
	}
}
