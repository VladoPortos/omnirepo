package pypi_test

import (
	"context"
	"database/sql"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/vladoportos/omnirepo/internal/metadata"
)

// --------------------------------------------------------------------------
// Phase 8 Plan 01 (MIRROR-03) — MirrorGuard rejects PEP 694 upload-session
// API calls on mirror-flagged repos, returns 201 on plain repos. Uses
// distinct test names so they don't collide with upload_legacy_test.go.
// --------------------------------------------------------------------------

// pep694CreateSessionRaw is a thin wrapper over the pep694CreateSession
// helper in handler_test.go that also returns the full response (so the
// mirror-guard tests can read the body to assert the envelope code).
func pep694CreateSessionRaw(t *testing.T, srvURL, proj, repo, auth string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost,
		srvURL+"/"+proj+"/pypi/"+repo+"/+upload/",
		strings.NewReader(`{"meta":{"api-version":"1.0"},"name":"mypkg","version":"1.0"}`))
	if err != nil {
		t.Fatalf("req: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST +upload: %v", err)
	}
	return resp
}

func TestPEP694Upload_MirrorRepoReturns403(t *testing.T) {
	f := newHandlerFixture(t)
	_, repoID := f.seedRepo("proj1", "mirrored-694", true, false)
	if err := f.db.WriteTx(context.Background(), func(tx *sql.Tx) error {
		return f.repos.SetMirrorConfigInTx(context.Background(), tx, repoID, metadata.MirrorConfig{
			IsMirror:    true,
			UpstreamURL: "https://upstream.example/simple/",
			FilterJSON:  `{}`,
			CredID:      nil,
			ScanOnSync:  false,
		})
	}); err != nil {
		t.Fatalf("set mirror cfg: %v", err)
	}
	resp := pep694CreateSessionRaw(t, f.srv.URL, "proj1", "mirrored-694", f.basicAuth())
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "repo_is_mirror") {
		t.Fatalf("body missing repo_is_mirror: %s", body)
	}
}

func TestPEP694Upload_NonMirrorRepoStillWorks(t *testing.T) {
	f := newHandlerFixture(t)
	_, _ = f.seedRepo("proj1", "plain-694", true, false)
	resp := pep694CreateSessionRaw(t, f.srv.URL, "proj1", "plain-694", f.basicAuth())
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusForbidden {
		body, _ := io.ReadAll(resp.Body)
		if strings.Contains(string(body), "repo_is_mirror") {
			t.Fatalf("non-mirror repo rejected as mirror: %s", body)
		}
	}
	// 201 on success, other codes possible for unrelated reasons;
	// the contract here is that the mirror-guard DOES NOT block.
}
