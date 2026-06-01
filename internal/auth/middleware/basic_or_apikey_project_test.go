package middleware_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vladoportos/omnirepo/internal/auth"
	"github.com/vladoportos/omnirepo/internal/auth/middleware"
	"github.com/vladoportos/omnirepo/internal/metadata"
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

	pid, err := projects.Create(context.Background(), "acme", "")
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
		ProjectName: "acme",
		ProjectKey:  k,
		Projects:    projects,
	}
}

// Test 3: project:<proj>:<omr_p_xxx> → project-scoped actor.
// Wire format: Basic base64("project:<projname>:<omr_p_...>")
// Go's BasicAuth splits on first ":", so login="project", pw="<projname>:<key>".
func TestBasicProjectVariant_Success(t *testing.T) {
	e := newProjectEnv(t)
	h := middleware.BasicOrAPIKey(e.Deps)(okHandler())

	// login="project", password="acme:<key>" — produces base64("project:acme:<key>")
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", basicAuthHeader("project", "acme:"+e.ProjectKey.Plaintext))
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

	// Try authenticating as project "acme" using the key owned by "other"
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", basicAuthHeader("project", "acme:"+k2.Plaintext))
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

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", basicAuthHeader("project", "nonexistent:"+e.ProjectKey.Plaintext))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401 (nonexistent project)", w.Code)
	}
}

// TestBasicProjectVariant_ThreadsMintedRole is the B-1 regression: the project
// Basic-auth path must copy api_keys.role into Actor.APIKeyRole. Before the fix
// it left APIKeyRole="" for every project key, and ResolveMembership defaulted
// the empty role to "maintainer" — silently escalating read-only viewer keys to
// write/push/admin. A NULL role (legacy/pre-RBAC key) must still default to
// "maintainer" for backward compatibility.
func TestBasicProjectVariant_ThreadsMintedRole(t *testing.T) {
	e := newProjectEnv(t)

	// Seed a viewer-role key for the existing project.
	viewerKey, err := auth.GenerateAPIKey(auth.APIKeyKindProject)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.APIKeys.CreateProjectKeyWithRole(
		context.Background(), e.ProjectID, "scraper", viewerKey.Prefix, viewerKey.SHA256, "viewer",
	); err != nil {
		t.Fatalf("seed viewer key: %v", err)
	}

	var gotRole string
	var gotScoped bool
	roleHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a, ok := auth.ActorFromContext(r.Context())
		gotScoped = ok && a.ProjectScope != nil
		gotRole = a.APIKeyRole
		w.WriteHeader(http.StatusOK)
	})
	h := middleware.BasicOrAPIKey(e.Deps)(roleHandler)

	// viewer key → APIKeyRole must be "viewer", NOT the maintainer fallback.
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", basicAuthHeader("project", e.ProjectName+":"+viewerKey.Plaintext))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("viewer key status=%d want 200", w.Code)
	}
	if !gotScoped {
		t.Fatal("actor not project-scoped")
	}
	if gotRole != "viewer" {
		t.Fatalf("APIKeyRole=%q want %q (role-threading regression — viewer key escalated)", gotRole, "viewer")
	}

	// Legacy NULL-role key (seeded by newProjectEnv via CreateProjectKey) must
	// still resolve to the "maintainer" fallback for backward compatibility.
	req2 := httptest.NewRequest("GET", "/", nil)
	req2.Header.Set("Authorization", basicAuthHeader("project", e.ProjectName+":"+e.ProjectKey.Plaintext))
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("legacy key status=%d want 200", w2.Code)
	}
	if gotRole != "maintainer" {
		t.Fatalf("legacy NULL-role key APIKeyRole=%q want %q (backward-compat fallback)", gotRole, "maintainer")
	}
}

// Test 6: No auth header on /git/... → 401 + WWW-Authenticate: Basic
// (This test verifies that the basic middleware returns 401 on missing auth.)
func TestBasicNoAuth_Returns401(t *testing.T) {
	e := newProjectEnv(t)
	h := middleware.BasicOrAPIKey(e.Deps)(okHandler())

	req := httptest.NewRequest("GET", "/git/acme/myrepo.git/info/refs", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401", w.Code)
	}
}
