package pypi_test

import (
	"context"
	"database/sql"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/dxc-internal/omnirepo/internal/metadata"
)

// --------------------------------------------------------------------------
// Phase 8 Plan 01 (MIRROR-03) — MirrorGuard rejects PyPI legacy twine uploads
// on mirror-flagged repos, returns 201 on plain repos.
// --------------------------------------------------------------------------

func TestUpload_MirrorRepoReturns403(t *testing.T) {
	f := newHandlerFixture(t)
	_, repoID := f.seedRepo("proj1", "mirrored", true, false)
	if err := f.db.WriteTx(context.Background(), func(tx *sql.Tx) error {
		return f.repos.SetMirrorConfigInTx(context.Background(), tx, repoID, metadata.MirrorConfig{
			IsMirror:    true,
			UpstreamURL: "https://pypi.org/simple/",
			FilterJSON:  `{}`,
			CredID:      nil,
			ScanOnSync:  false,
		})
	}); err != nil {
		t.Fatalf("set mirror cfg: %v", err)
	}

	wheel := makeWheelBytes(t, "mypkg", "1.0")
	resp := twineUpload(t, f.srv.URL, "proj1", "mirrored", "mypkg-1.0-py3-none-any.whl", wheel, "bdist_wheel", f.basicAuth())
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "repo_is_mirror") {
		t.Fatalf("body missing repo_is_mirror: %s", body)
	}
}

func TestUpload_NonMirrorRepoStillWorks(t *testing.T) {
	f := newHandlerFixture(t)
	_, _ = f.seedRepo("proj1", "plain", true, false)
	wheel := makeWheelBytes(t, "mypkg", "1.0")
	resp := twineUpload(t, f.srv.URL, "proj1", "plain", "mypkg-1.0-py3-none-any.whl", wheel, "bdist_wheel", f.basicAuth())
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 201/200 (non-mirror); body=%s", resp.StatusCode, body)
	}
}

// TestDeletePackage_MirrorRepoReturns403 — plan 08-06 Codex rescue Q3a.
// DELETE /pypi/<repo>/packages/<filename> is a mutating operation and
// must be rejected on mirror-flagged repos by MirrorGuardFixed. Before
// this fix, the route sat outside the guard group.
func TestDeletePackage_MirrorRepoReturns403(t *testing.T) {
	f := newHandlerFixture(t)
	_, repoID := f.seedRepo("proj1", "mirrored-del", true, false)
	if err := f.db.WriteTx(context.Background(), func(tx *sql.Tx) error {
		return f.repos.SetMirrorConfigInTx(context.Background(), tx, repoID, metadata.MirrorConfig{
			IsMirror:    true,
			UpstreamURL: "https://pypi.org/simple/",
			FilterJSON:  `{}`,
			CredID:      nil,
			ScanOnSync:  false,
		})
	}); err != nil {
		t.Fatalf("set mirror cfg: %v", err)
	}
	url := f.srv.URL + "/proj1/pypi/mirrored-del/packages/mypkg-1.0-py3-none-any.whl"
	req, _ := http.NewRequest(http.MethodDelete, url, nil)
	req.Header.Set("Authorization", f.basicAuth())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 403; body=%s", resp.StatusCode, body)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "repo_is_mirror") {
		t.Fatalf("body missing repo_is_mirror: %s", body)
	}
}
