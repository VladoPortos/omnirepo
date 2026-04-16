package api_test

import (
	"context"
	"database/sql"
	"net/http"
	"testing"

	"github.com/dxc-internal/omnirepo/internal/metadata"
)

func TestSearch_EmptyQuery(t *testing.T) {
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
	cookie, _, code := s.login(t, "root", pw)
	if code != 200 {
		t.Fatalf("login code=%d", code)
	}

	resp, body := s.do(t, "GET", "/api/v1/search", cookie, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("code=%d body=%v", resp.StatusCode, body)
	}
	items, ok := body["items"].([]any)
	if !ok {
		t.Fatalf("expected items array, got %v", body)
	}
	if len(items) != 0 {
		t.Fatalf("expected 0 items for empty query, got %d", len(items))
	}
}

// seedRepoWithFTS creates a project + repo and populates repos_fts so search works.
func seedRepoWithFTS(t *testing.T, db *metadata.DB, projectName, repoType, repoName, desc string) (int64, int64) {
	t.Helper()
	ctx := context.Background()
	pid, err := metadata.NewProjectsRepo(db).Create(ctx, projectName, "")
	if err != nil {
		t.Fatal(err)
	}
	var repoID int64
	err = db.WriteTx(ctx, func(tx *sql.Tx) error {
		var ie error
		repoID, ie = metadata.NewReposRepo(db).CreateInTx(ctx, tx, pid, repoType, repoName, desc, nil, nil, nil)
		if ie != nil {
			return ie
		}
		return metadata.IndexRepo(ctx, tx, repoID, repoName, projectName, desc, repoType)
	})
	if err != nil {
		t.Fatal(err)
	}
	return pid, repoID
}

func TestSearch_WithFTSData(t *testing.T) {
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
	cookie, _, code := s.login(t, "root", pw)
	if code != 200 {
		t.Fatalf("login code=%d", code)
	}

	seedRepoWithFTS(t, s.db, "myproject", "docker", "myrepo", "a docker repo")

	resp, body := s.do(t, "GET", "/api/v1/search?q=myrepo", cookie, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("code=%d body=%v", resp.StatusCode, body)
	}
	items, ok := body["items"].([]any)
	if !ok {
		t.Fatalf("expected items array, got %v", body)
	}
	if len(items) == 0 {
		t.Fatal("expected search results for 'myrepo', got 0")
	}
	first := items[0].(map[string]any)
	if first["kind"] != "repo" {
		t.Fatalf("expected kind=repo, got %v", first["kind"])
	}
}

func TestSearch_FilteredByMembership(t *testing.T) {
	s := newTestServer(t)
	_, rootPW := seedTestUser(t, s.db, "root", "r@x", true, false)
	_, alicePW := seedTestUser(t, s.db, "alice", "a@x", false, false)

	seedRepoWithFTS(t, s.db, "secret-proj", "docker", "hidden-repo", "secret stuff")

	// Root (super-admin) should see it.
	rootCookie, _, _ := s.login(t, "root", rootPW)
	_, rootBody := s.do(t, "GET", "/api/v1/search?q=hidden-repo", rootCookie, nil)
	rootItems := rootBody["items"].([]any)
	if len(rootItems) == 0 {
		t.Fatal("super-admin should see search result")
	}

	// Alice (non-member) should NOT see it.
	aliceCookie, _, _ := s.login(t, "alice", alicePW)
	_, aliceBody := s.do(t, "GET", "/api/v1/search?q=hidden-repo", aliceCookie, nil)
	aliceItems := aliceBody["items"].([]any)
	if len(aliceItems) != 0 {
		t.Fatalf("non-member should see 0 results, got %d", len(aliceItems))
	}
}
