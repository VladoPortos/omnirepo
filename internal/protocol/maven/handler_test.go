package maven_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/vladoportos/omnirepo/internal/audit"
	"github.com/vladoportos/omnirepo/internal/auth"
	"github.com/vladoportos/omnirepo/internal/metadata"
	"github.com/vladoportos/omnirepo/internal/metadata/sqlitetest"
	"github.com/vladoportos/omnirepo/internal/protocol/maven"
	"github.com/vladoportos/omnirepo/internal/storage"
)

type fixture struct {
	t         *testing.T
	db        *metadata.DB
	repos     *metadata.ReposRepo
	projects  *metadata.ProjectsRepo
	artifacts *metadata.MavenArtifactsRepo
	srv       *httptest.Server
	login     string
	password  string
	userID    int64
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	db := sqlitetest.New(t)
	users := metadata.NewUsersRepo(db)
	apiKeys := metadata.NewAPIKeysRepo(db)
	sessions := metadata.NewSessionsRepo(db)
	repos := metadata.NewReposRepo(db)
	projects := metadata.NewProjectsRepo(db)
	artifacts := metadata.NewMavenArtifactsRepo(db)

	login := "mvn-user"
	password := "mvn-test-password-1234567"
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatalf("hash pw: %v", err)
	}
	uid, err := users.Create(context.Background(), login, "m@example.com", hash, false, false)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	dataRoot := t.TempDir()
	repoRoot := filepath.Join(dataRoot, "repos")
	trashRoot := filepath.Join(dataRoot, "trash")
	for _, d := range []string{repoRoot, trashRoot, filepath.Join(dataRoot, "logs")} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	auditLogger, err := audit.New(db, filepath.Join(dataRoot, "logs", "audit.log"), 10, 1)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}

	h := maven.New(maven.Deps{
		DB:          db,
		Users:       users,
		APIKeys:     apiKeys,
		Sessions:    sessions,
		Repos:       repos,
		Projects:    projects,
		Members:     metadata.NewMembersRepo(db),
		Artifacts:   artifacts,
		Path:        storage.NewPathStore(repoRoot),
		Trash:       storage.NewTrash(trashRoot),
		Audit:       auditLogger,
		MaxPutBytes: 16 << 20,
		RepoRoot:    repoRoot,
	})

	r := chi.NewRouter()
	h.Mount(r)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	return &fixture{
		t: t, db: db, repos: repos, projects: projects, artifacts: artifacts,
		srv: srv, login: login, password: password, userID: uid,
	}
}

func (f *fixture) seedRepo(projName, repoName string, publicRead bool) (projectID, repoID int64) {
	pid, err := f.projects.Create(context.Background(), projName, "test")
	if err != nil {
		f.t.Fatalf("seed project: %v", err)
	}
	if _, err := f.db.Writer.Exec(`INSERT INTO project_members(project_id, user_id) VALUES (?, ?)`, pid, f.userID); err != nil {
		f.t.Fatalf("seed member: %v", err)
	}
	autoScan := false
	rid, err := f.repos.Create(context.Background(), pid, "maven", repoName, "", &autoScan, nil, &publicRead)
	if err != nil {
		f.t.Fatalf("seed repo: %v", err)
	}
	return pid, rid
}

func (f *fixture) do(t *testing.T, method, urlPath string, body []byte, withAuth bool) *http.Response {
	t.Helper()
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, _ := http.NewRequest(method, f.srv.URL+urlPath, rd)
	if withAuth {
		req.Header.Set("Authorization",
			"Basic "+base64.StdEncoding.EncodeToString([]byte(f.login+":"+f.password)))
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, urlPath, err)
	}
	return resp
}

func mustBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(b)
}

// TestDeployAndFetchRoundtrip mirrors what mvn deploy does: PUT the jar,
// pom, checksum sidecars, and maven-metadata.xml, then GET them back.
func TestDeployAndFetchRoundtrip(t *testing.T) {
	f := newFixture(t)
	_, repoID := f.seedRepo("acme", "libs", false)
	base := "/acme/maven/libs/com/acme/mini/1.0.0/"

	files := map[string][]byte{
		"mini-1.0.0.jar":      []byte("jar-bytes"),
		"mini-1.0.0.jar.sha1": []byte("deadbeef"),
		"mini-1.0.0.pom":      []byte("<project/>"),
		"mini-1.0.0.pom.sha1": []byte("deadbeef"),
	}
	for name, content := range files {
		resp := f.do(t, http.MethodPut, base+name, content, true)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("PUT %s = %d body=%s", name, resp.StatusCode, mustBody(t, resp))
		}
		_ = resp.Body.Close()
	}
	// Artifact-level metadata, as the deploy plugin uploads it.
	resp := f.do(t, http.MethodPut, "/acme/maven/libs/com/acme/mini/maven-metadata.xml",
		[]byte("<metadata/>"), true)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT metadata = %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// Everything reads back byte-identical (incl. HEAD).
	for name, content := range files {
		resp := f.do(t, http.MethodGet, base+name, nil, true)
		if body := mustBody(t, resp); resp.StatusCode != 200 || body != string(content) {
			t.Errorf("GET %s = %d %q", name, resp.StatusCode, body)
		}
	}
	resp = f.do(t, http.MethodHead, base+"mini-1.0.0.jar", nil, true)
	if resp.StatusCode != http.StatusOK || resp.ContentLength != int64(len("jar-bytes")) {
		t.Errorf("HEAD = %d len=%d", resp.StatusCode, resp.ContentLength)
	}
	_ = resp.Body.Close()

	// Rows exist for jar + pom ONLY (checksums + metadata are disk-only).
	rows, err := f.artifacts.ListByRepo(context.Background(), repoID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2 (jar+pom): %+v", len(rows), rows)
	}
	for _, a := range rows {
		if a.GroupID != "com.acme" || a.ArtifactID != "mini" || a.Version != "1.0.0" {
			t.Errorf("GAV parse wrong: %+v", a)
		}
	}
}

func TestClassifierParsing(t *testing.T) {
	f := newFixture(t)
	_, repoID := f.seedRepo("acme", "libs", false)
	resp := f.do(t, http.MethodPut,
		"/acme/maven/libs/com/acme/mini/1.0.0/mini-1.0.0-sources.jar", []byte("src"), true)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT = %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	row, err := f.artifacts.FindByPath(context.Background(), repoID, "com/acme/mini/1.0.0/mini-1.0.0-sources.jar")
	if err != nil {
		t.Fatal(err)
	}
	if row.Classifier != "sources" || row.Extension != "jar" {
		t.Errorf("classifier/ext = %q/%q", row.Classifier, row.Extension)
	}
}

func TestRedeployOverwrites(t *testing.T) {
	f := newFixture(t)
	_, repoID := f.seedRepo("acme", "libs", false)
	p := "/acme/maven/libs/com/acme/mini/1.0-SNAPSHOT/mini-1.0-SNAPSHOT.jar"

	for _, content := range []string{"v1-bytes", "v2-bytes-longer"} {
		resp := f.do(t, http.MethodPut, p, []byte(content), true)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("PUT = %d", resp.StatusCode)
		}
		_ = resp.Body.Close()
	}
	resp := f.do(t, http.MethodGet, p, nil, true)
	if body := mustBody(t, resp); body != "v2-bytes-longer" {
		t.Errorf("redeploy content = %q", body)
	}
	rows, _ := f.artifacts.ListByRepo(context.Background(), repoID)
	if len(rows) != 1 || rows[0].SizeBytes != int64(len("v2-bytes-longer")) {
		t.Errorf("redeploy rows = %+v", rows)
	}
}

func TestAuthAndValidation(t *testing.T) {
	f := newFixture(t)
	f.seedRepo("acme", "libs", false)

	// Unauthenticated write.
	resp := f.do(t, http.MethodPut, "/acme/maven/libs/com/acme/x/1/x-1.jar", []byte("b"), false)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("unauthenticated PUT = %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// Traversal and invalid paths.
	for _, p := range []string{
		"/acme/maven/libs/../../../etc/passwd",
		"/acme/maven/libs/com/acme/%2e%2e/x.jar",
		"/acme/maven/libs/com//acme/x.jar",
	} {
		resp := f.do(t, http.MethodPut, p, []byte("b"), true)
		if resp.StatusCode != http.StatusBadRequest && resp.StatusCode != http.StatusNotFound {
			t.Errorf("PUT %s = %d, want 400/404", p, resp.StatusCode)
		}
		_ = resp.Body.Close()
	}
}

func TestDeleteArtifact(t *testing.T) {
	f := newFixture(t)
	_, repoID := f.seedRepo("acme", "libs", false)
	p := "/acme/maven/libs/com/acme/mini/1.0.0/mini-1.0.0.jar"
	resp := f.do(t, http.MethodPut, p, []byte("jar"), true)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT = %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	resp = f.do(t, http.MethodDelete, p, nil, true)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE = %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	resp = f.do(t, http.MethodGet, p, nil, true)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET after delete = %d", resp.StatusCode)
	}
	_ = resp.Body.Close()
	if _, err := f.artifacts.FindByPath(context.Background(), repoID, "com/acme/mini/1.0.0/mini-1.0.0.jar"); err == nil {
		t.Errorf("row still present after delete")
	}

	// Second delete 404s.
	resp = f.do(t, http.MethodDelete, p, nil, true)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("double delete = %d", resp.StatusCode)
	}
	_ = resp.Body.Close()
}

func TestAnonymousReadFollowsPublicRead(t *testing.T) {
	f := newFixture(t)
	f.seedRepo("pub", "open", true)
	p := "/pub/maven/open/com/acme/mini/1.0.0/mini-1.0.0.jar"
	resp := f.do(t, http.MethodPut, p, []byte("jar"), true)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT = %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	resp = f.do(t, http.MethodGet, p, nil, false)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("anon public GET = %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	f2 := newFixture(t)
	f2.seedRepo("priv", "closed", false)
	resp = f2.do(t, http.MethodPut, "/priv/maven/closed/com/acme/mini/1.0.0/mini-1.0.0.jar", []byte("jar"), true)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT = %d", resp.StatusCode)
	}
	_ = resp.Body.Close()
	resp = f2.do(t, http.MethodGet, "/priv/maven/closed/com/acme/mini/1.0.0/mini-1.0.0.jar", nil, false)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("anon private GET = %d, want 401", resp.StatusCode)
	}
	_ = resp.Body.Close()
}
