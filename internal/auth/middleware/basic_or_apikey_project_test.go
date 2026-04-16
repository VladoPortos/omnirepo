package middleware_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dxc-internal/omnirepo/internal/auth"
	"github.com/dxc-internal/omnirepo/internal/auth/middleware"
	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/metadata/sqlitetest"
)

// projectEnv extends the base testEnv with project-scoped fixtures.
type projectEnv struct {
	*testEnv
	ProjectID   int64
	ProjectName string
	ProjectKey  auth.APIKey
	Projects    *metadata.ProjectsRepo
}

func newProjectEnv(t *testing.T) *projectEnv {
	t.Helper()
	base := newEnv(t)
	projects := metadata.NewProjectsRepo(base.DB)

	pid, err := projects.Create(context.Background(), "dxc", "")
	if err != nil {
		t.Fatalf("seed project: %v", err)
	}

	// Seed project-scoped API key.
	k, err := auth.GenerateAPIKey(auth.APIKeyKindProject)
	if err != nil {
		t.Fatalf("gen project key: %v", err)
	}
	if _, err := base.APIKeys.CreateProjectKey(context.Background(), pid, "ci-key", k.Prefix, k.SHA256); err != nil {
		t.Fatalf("seed project key: %v", err)
	}

	// Update Deps to include Projects
	base.Deps.Projects = projects

	return &projectEnv{
		testEnv:     base,
		ProjectID:   pid,
		ProjectName: "dxc",
		ProjectKey:  k,
		Projects:    projects,
	}
}

// Test 3: project:<proj>:<omr_p_xxx> → project-scoped actor.
func TestBasicProjectVariant_Success(t *testing.T) {
	e := newProjectEnv(t)
	h := middleware.BasicOrAPIKey(e.Deps)(okHandler())

	login := "project:dxc"
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", basicAuthHeader(login, e.ProjectKey.Plaintext))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
	}
	// The actor should be a project-owned API key actor with ProjectScope.
	if !strings.Contains(w.Body.String(), "ok,login=") {
		t.Fatalf("body=%q", w.Body.String())
	}
}

// Test 4: project:<projA>:<key-for-projB> → 401 (mismatch).
func TestBasicProjectVariant_MismatchProject(t *testing.T) {
	e := newProjectEnv(t)

	// Create a second project + key for that project.
	pid2, err := e.Projects.Create(context.Background(), "other", "")
	if err != nil {
		t.Fatal(err)
	}
	k2, err := auth.GenerateAPIKey(auth.APIKeyKindProject)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.APIKeys.CreateProjectKey(context.Background(), pid2, "ci2", k2.Prefix, k2.SHA256); err != nil {
		t.Fatal(err)
	}

	h := middleware.BasicOrAPIKey(e.Deps)(okHandler())

	// Try authenticating as project "dxc" using the key owned by "other"
	login := "project:dxc"
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", basicAuthHeader(login, k2.Plaintext))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401 (project mismatch)", w.Code)
	}
}

// Test 5: project:<nonexistent>:<key> → 401.
func TestBasicProjectVariant_NonexistentProject(t *testing.T) {
	e := newProjectEnv(t)
	h := middleware.BasicOrAPIKey(e.Deps)(okHandler())

	login := "project:nonexistent"
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", basicAuthHeader(login, e.ProjectKey.Plaintext))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401 (nonexistent project)", w.Code)
	}
}

// Test 6: No auth header on /git/... → 401 + WWW-Authenticate: Basic
// (This test verifies that the basic middleware returns 401 on missing auth.)
func TestBasicNoAuth_Returns401(t *testing.T) {
	e := newProjectEnv(t)
	h := middleware.BasicOrAPIKey(e.Deps)(okHandler())

	req := httptest.NewRequest("GET", "/git/dxc/myrepo.git/info/refs", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401", w.Code)
	}
}
