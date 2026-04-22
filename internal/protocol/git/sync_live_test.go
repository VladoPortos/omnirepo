//go:build live_git

// Live E2E for GITMIRROR-09: mirror a real public GitHub HTTPS repo end-to-end.
//
// Gated behind the `live_git` build tag and intended to run only under
// `make test-live-git`. No credentials required — the target repo is public.
// If env var LIVE_GIT_UPSTREAM is set, use that instead of the default
// (useful when GitHub is unreachable in air-gapped pre-release runs;
// operators can point at an internal GitLab or Gitea HTTPS mirror).
//
// Scope guard (plan 11-08, D-17): keep to a single end-to-end scenario.
//
//  1. Create a type=git, is_mirror=true repo fixture (reusing helpers
//     from sync_handler_test.go that already build the in-memory SQLite
//     DB, the mirror repos row, and a fully-wired SyncHandler).
//  2. Pre-flight HEAD probe of the upstream URL — if unreachable, t.Skip.
//  3. handler.Handle(); assert no error; verify bare repo + non-zero refs.
//  4. handler.Handle() again (FetchContext path / NoErrAlreadyUpToDate);
//     assert no error.
//  5. Assert git_refs rows > 0 in the test DB.
//  6. Assert audit emitted EvtSyncStarted + EvtSyncFinished at least once
//     each (the second sync may legitimately produce a second pair).
//
// Anti-scope: do NOT assert specific ref names or commit shas — GitHub
// repos move (new tags/branches land continuously). The hermetic tests
// in plan 11-06 prove correctness; this test only proves real-network
// plumbing works end-to-end against a real Git server.
//
// Pitfall E (per plan 11-06 SUMMARY): the SyncHandler's TLS/CA/proxy/
// timeout config rides on the shared *http.Client passed via
// HTTPClient. http.DefaultClient is used here because the live target
// is a vanilla public HTTPS endpoint with system-default TLS — no
// custom CA bundle needed.

package git_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"

	"github.com/dxc-internal/omnirepo/internal/audit"
	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/metadata/sqlitetest"
	gitpkg "github.com/dxc-internal/omnirepo/internal/protocol/git"
)

// liveGitDefaultUpstream is locked to the planner's D-17 pick: a small,
// stable, LFS-free public GitHub repo (~4 MB, pure Python, stable since
// 2014). Operators can override via LIVE_GIT_UPSTREAM.
const liveGitDefaultUpstream = "https://github.com/pallets/click.git"

// TestLiveGitHubMirrorSync runs the full sync handler against a real
// public GitHub repo. Requires outbound HTTPS to github.com (or to
// LIVE_GIT_UPSTREAM if set). Skips cleanly when the upstream is
// unreachable so this never fakes a green run on an air-gapped builder.
func TestLiveGitHubMirrorSync(t *testing.T) {
	upstream := os.Getenv("LIVE_GIT_UPSTREAM")
	if upstream == "" {
		upstream = liveGitDefaultUpstream
	}

	// Pre-flight connectivity probe — short timeout so the test fails
	// fast (and skips) when the network is unavailable. GitHub serves a
	// 301 redirect on HEAD of the .git URL; that's still a success
	// signal for "the host is reachable + responding".
	probeCtx, probeCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer probeCancel()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodHead, upstream, nil)
	if err != nil {
		t.Skipf("live_git skipping: malformed upstream %q: %v", upstream, err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Skipf("live_git skipping: upstream %s unreachable: %v", upstream, err)
	}
	_ = resp.Body.Close()

	// Generous timeout: a clone of pallets/click (~4 MB) typically
	// finishes in <30 s on a corporate link, but we allow plenty of
	// headroom for slow CI runners and TLS handshake variability.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Reuse helpers from sync_handler_test.go (same package, same
	// build context — they compile together when the live_git tag is
	// set because untagged files are always part of the build).
	db := sqlitetest.New(t)
	projectID, repoID := seedMirrorRepo(t, db, "live-gh", "click", upstream)
	h, rec, dataRoot := newSyncHandlerForTest(t, db)

	payload, _ := json.Marshal(gitpkg.SyncPayload{UpstreamURL: upstream}) // anonymous

	// First sync: PlainCloneContext path.
	if err := h.Handle(ctx, string(payload), projectID, repoID, 1); err != nil {
		t.Fatalf("first sync (clone): %v", err)
	}

	// Verify the bare repo landed on disk and contains references.
	bareDir := filepath.Join(dataRoot, "repos", "live-gh", "git", "click.git")
	if _, err := os.Stat(filepath.Join(bareDir, "config")); err != nil {
		t.Fatalf("bare repo config missing after clone: %v", err)
	}
	repo, err := gogit.PlainOpen(bareDir)
	if err != nil {
		t.Fatalf("PlainOpen(%s): %v", bareDir, err)
	}
	iter, err := repo.References()
	if err != nil {
		t.Fatalf("References(): %v", err)
	}
	var refCount int
	if err := iter.ForEach(func(_ *plumbing.Reference) error {
		refCount++
		return nil
	}); err != nil {
		t.Fatalf("References ForEach: %v", err)
	}
	iter.Close()
	if refCount == 0 {
		t.Fatal("expected >0 refs after clone of pallets/click; got 0")
	}

	// Verify git_refs rows landed in the test DB.
	dbRefs, err := metadata.NewGitRefsRepo(db).List(ctx, repoID)
	if err != nil {
		t.Fatalf("git_refs List: %v", err)
	}
	if len(dbRefs) == 0 {
		t.Fatal("expected git_refs rows > 0 after clone")
	}

	// Re-run: FetchContext path. NoErrAlreadyUpToDate is NOT a failure;
	// the handler swallows it.
	if err := h.Handle(ctx, string(payload), projectID, repoID, 2); err != nil {
		t.Fatalf("re-sync (fetch): %v", err)
	}

	// Audit assertions: at least one EvtSyncStarted + EvtSyncFinished
	// must have been emitted across the two Handle calls. The handler
	// emits one pair per Handle call, so we expect 2 of each on a
	// fully successful run — but we only assert "at least one" to keep
	// the test resilient to handler-internal restructuring (e.g. a
	// future plan that introduces retries).
	if rec.firstOf(audit.EvtSyncStarted) == nil {
		t.Fatalf("missing EvtSyncStarted; events: %v", auditKinds(rec.events))
	}
	if rec.firstOf(audit.EvtSyncFinished) == nil {
		t.Fatalf("missing EvtSyncFinished; events: %v", auditKinds(rec.events))
	}
	if rec.firstOf(audit.EvtSyncFailed) != nil {
		t.Errorf("unexpected EvtSyncFailed during live mirror: %+v", rec.firstOf(audit.EvtSyncFailed))
	}
}
