package httpx_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/vladoportos/omnirepo/internal/httpx"
	"github.com/vladoportos/omnirepo/internal/metadata"
	"github.com/vladoportos/omnirepo/internal/metadata/sqlitetest"
)

// --------------------------------------------------------------------------
// Phase 8 Plan 01 (MIRROR-03) — MirrorGuard unit tests.
// --------------------------------------------------------------------------

// guardTestServer wires a tiny chi router using MirrorGuard (or the Fixed
// variant) + a trivial OK handler so tests can drive status codes.
func guardTestServer(t *testing.T, fixed string) (*httptest.Server, *metadata.ReposRepo, *metadata.ProjectsRepo, *metadata.DB) {
	t.Helper()
	db := sqlitetest.New(t)
	projects := metadata.NewProjectsRepo(db)
	repos := metadata.NewReposRepo(db)

	r := chi.NewRouter()
	var mw func(http.Handler) http.Handler
	if fixed == "" {
		mw = httpx.MirrorGuard(repos, projects)
	} else {
		mw = httpx.MirrorGuardFixed(repos, projects, fixed)
	}
	next := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	})
	if fixed == "" {
		r.With(mw).Put("/{project}/{type}/{repo}/upload", next)
	} else {
		r.With(mw).Put("/{project}/x/{repo}/upload", next)
	}
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv, repos, projects, db
}

func TestMirrorGuard_MirrorRepoReturns403(t *testing.T) {
	srv, repos, projects, db := guardTestServer(t, "")
	ctx := context.Background()
	pid, err := projects.Create(ctx, "p1", "")
	if err != nil {
		t.Fatal(err)
	}
	rid, err := repos.Create(ctx, pid, "deb", "r1", "", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		return repos.SetMirrorConfigInTx(ctx, tx, rid, metadata.MirrorConfig{
			IsMirror:    true,
			UpstreamURL: "https://up.example",
			FilterJSON:  `{}`,
		})
	}); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/p1/deb/r1/upload", strings.NewReader(""))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
	var env map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&env)
	code, _ := env["code"].(string)
	if !strings.Contains(code, "repo_is_mirror") {
		t.Fatalf("code = %q, want *repo_is_mirror; body=%+v", code, env)
	}
}

func TestMirrorGuard_NonMirrorPassesThrough(t *testing.T) {
	srv, repos, projects, _ := guardTestServer(t, "")
	ctx := context.Background()
	pid, err := projects.Create(ctx, "p1", "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = repos.Create(ctx, pid, "deb", "plain", "", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/p1/deb/plain/upload", strings.NewReader(""))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (pass-through)", resp.StatusCode)
	}
}

func TestMirrorGuard_MissingRepoPassesThrough(t *testing.T) {
	srv, _, _, _ := guardTestServer(t, "")
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/nope/deb/missing/upload", strings.NewReader(""))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	// Guard passes through; the downstream handler here returns 200
	// regardless — the assertion is that we do NOT get a 403.
	if resp.StatusCode == http.StatusForbidden {
		t.Fatalf("missing repo returned 403 unexpectedly")
	}
}

func TestMirrorGuardFixed_MirrorRepoReturns403(t *testing.T) {
	srv, repos, projects, db := guardTestServer(t, "deb")
	ctx := context.Background()
	pid, err := projects.Create(ctx, "p1", "")
	if err != nil {
		t.Fatal(err)
	}
	rid, err := repos.Create(ctx, pid, "deb", "r1", "", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		return repos.SetMirrorConfigInTx(ctx, tx, rid, metadata.MirrorConfig{
			IsMirror:    true,
			UpstreamURL: "https://up.example",
			FilterJSON:  `{}`,
		})
	}); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/p1/x/r1/upload", strings.NewReader(""))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
}

func TestMirrorGuardFixed_NonMirrorPassesThrough(t *testing.T) {
	srv, repos, projects, _ := guardTestServer(t, "deb")
	ctx := context.Background()
	pid, err := projects.Create(ctx, "p1", "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = repos.Create(ctx, pid, "deb", "plain", "", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/p1/x/plain/upload", strings.NewReader(""))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

func TestMirrorGuardFixed_MissingRepoPassesThrough(t *testing.T) {
	srv, _, _, _ := guardTestServer(t, "deb")
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/nope/x/missing/upload", strings.NewReader(""))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusForbidden {
		t.Fatalf("missing repo returned 403 unexpectedly")
	}
}
