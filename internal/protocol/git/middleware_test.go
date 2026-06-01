package git_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/vladoportos/omnirepo/internal/auth"
	"github.com/vladoportos/omnirepo/internal/metadata"
	"github.com/vladoportos/omnirepo/internal/metadata/sqlitetest"
	gitpkg "github.com/vladoportos/omnirepo/internal/protocol/git"
	"github.com/vladoportos/omnirepo/internal/storage"
)

// --- resolveRepoFromURL parses URL and stashes repo + permission ---

func TestResolveRepoFromURL_UploadPack(t *testing.T) {
	db := sqlitetest.New(t)
	projects := metadata.NewProjectsRepo(db)
	repos := metadata.NewReposRepo(db)

	pid, err := projects.Create(context.Background(), "acme", "")
	if err != nil {
		t.Fatal(err)
	}
	rid, err := repos.Create(context.Background(), pid, "git", "myrepo", "", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	mw := gitpkg.ResolveRepoFromURL(projects, repos)

	var captured *metadata.Repo
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = gitpkg.RepoFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	r := chi.NewRouter()
	r.Route("/git/{project}/{repo}", func(sub chi.Router) {
		sub.Use(mw)
		sub.Handle("/*", inner)
	})

	req := httptest.NewRequest("GET", "/git/acme/myrepo.git/info/refs?service=git-upload-pack", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
	}
	if captured == nil {
		t.Fatal("repo not stashed on context")
	}
	if captured.ID != rid {
		t.Fatalf("repo.ID=%d want %d", captured.ID, rid)
	}
}

// --- resolveRepoFromURL 404 on unknown project ---

func TestResolveRepoFromURL_UnknownProject(t *testing.T) {
	db := sqlitetest.New(t)
	projects := metadata.NewProjectsRepo(db)
	repos := metadata.NewReposRepo(db)

	mw := gitpkg.ResolveRepoFromURL(projects, repos)

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("inner handler should not be called")
	})

	r := chi.NewRouter()
	r.Route("/git/{project}/{repo}", func(sub chi.Router) {
		sub.Use(mw)
		sub.Handle("/*", inner)
	})

	// Anonymous callers get 401 + Basic challenge
	// for both missing and private repos so status-code sniffing can't
	// enumerate repo names.
	req := httptest.NewRequest("GET", "/git/unknown/other.git/info/refs", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous on unknown repo: status=%d want 401", w.Code)
	}
	if auth := w.Header().Get("WWW-Authenticate"); !strings.Contains(auth, "Basic") {
		t.Fatalf("expected Basic challenge, got WWW-Authenticate=%q", auth)
	}

	// Authenticated callers DO get a real 404 so they can distinguish
	// "repo doesn't exist" from "no permission" once they've logged in.
	req2 := httptest.NewRequest("GET", "/git/unknown/other.git/info/refs", nil)
	req2.SetBasicAuth("alice", "whatever")
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)
	if w2.Code != http.StatusNotFound {
		t.Fatalf("authenticated on unknown repo: status=%d want 404", w2.Code)
	}
}

// --- RequireGitPermission ---

func TestRequireGitPermission_MemberRead(t *testing.T) {
	actor := auth.Actor{ID: 10, Kind: auth.ActorKindUser}
	ctx := auth.WithProjectMembership(context.Background(), map[int64]string{42: "maintainer"})
	ctx = auth.WithActor(ctx, actor)

	repo := &metadata.Repo{ID: 1, ProjectID: 42}

	mw := gitpkg.RequireGitPermission()
	var called bool
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest("GET", "/git/acme/myrepo.git/info/refs?service=git-upload-pack", nil)
	req = req.WithContext(gitpkg.WithRepo(ctx, repo))
	w := httptest.NewRecorder()
	mw(inner).ServeHTTP(w, req)

	if !called {
		t.Fatal("inner not called; permission denied")
	}
}

func TestRequireGitPermission_MemberWrite(t *testing.T) {
	actor := auth.Actor{ID: 10, Kind: auth.ActorKindUser}
	ctx := auth.WithProjectMembership(context.Background(), map[int64]string{42: "maintainer"})
	ctx = auth.WithActor(ctx, actor)

	repo := &metadata.Repo{ID: 1, ProjectID: 42}

	mw := gitpkg.RequireGitPermission()
	var called bool
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest("POST", "/git/acme/myrepo.git/git-receive-pack", nil)
	req = req.WithContext(gitpkg.WithRepo(ctx, repo))
	w := httptest.NewRecorder()
	mw(inner).ServeHTTP(w, req)

	if !called {
		t.Fatal("inner not called; member write denied")
	}
}

func TestRequireGitPermission_NonMemberDenied(t *testing.T) {
	actor := auth.Actor{ID: 11, Kind: auth.ActorKindUser}
	ctx := auth.WithProjectMembership(context.Background(), map[int64]string{99: "maintainer"}) // member of 99, not 42
	ctx = auth.WithActor(ctx, actor)

	repo := &metadata.Repo{ID: 1, ProjectID: 42}

	mw := gitpkg.RequireGitPermission()
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not be called")
	})

	req := httptest.NewRequest("GET", "/git/acme/myrepo.git/info/refs?service=git-upload-pack", nil)
	req = req.WithContext(gitpkg.WithRepo(ctx, repo))
	w := httptest.NewRecorder()
	mw(inner).ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status=%d want 403", w.Code)
	}
}

// --- project-scoped API key actor ---

func TestRequireGitPermission_ProjectAPIKeyWrite(t *testing.T) {
	pid := int64(42)
	actor := auth.Actor{
		Kind:         auth.ActorKindAPIKey,
		OwnerKind:    auth.OwnerKindProject,
		ProjectScope: &pid,
	}
	ctx := auth.WithProjectMembership(context.Background(), map[int64]string{42: "maintainer"})
	ctx = auth.WithActor(ctx, actor)

	repo := &metadata.Repo{ID: 1, ProjectID: 42}

	mw := gitpkg.RequireGitPermission()
	var called bool
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest("POST", "/git/acme/myrepo.git/git-receive-pack", nil)
	req = req.WithContext(gitpkg.WithRepo(ctx, repo))
	w := httptest.NewRecorder()
	mw(inner).ServeHTTP(w, req)

	if !called {
		t.Fatal("project API key write on own project denied")
	}
}

func TestRequireGitPermission_ProjectAPIKeyCrossProject(t *testing.T) {
	pid := int64(99)
	actor := auth.Actor{
		Kind:         auth.ActorKindAPIKey,
		OwnerKind:    auth.OwnerKindProject,
		ProjectScope: &pid,
	}
	// Project scope is 99, but repo belongs to 42
	ctx := auth.WithProjectMembership(context.Background(), map[int64]string{99: "maintainer"})
	ctx = auth.WithActor(ctx, actor)

	repo := &metadata.Repo{ID: 1, ProjectID: 42}

	mw := gitpkg.RequireGitPermission()
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not be called")
	})

	req := httptest.NewRequest("POST", "/git/acme/myrepo.git/git-receive-pack", nil)
	req = req.WithContext(gitpkg.WithRepo(ctx, repo))
	w := httptest.NewRecorder()
	mw(inner).ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status=%d want 403", w.Code)
	}
}

// --- PerRepoMutex acquires lock on write ---

func TestPerRepoMutex_ReceivePackAcquiresLock(t *testing.T) {
	locks := storage.NewLocks()
	repo := &metadata.Repo{ID: 1, ProjectID: 42, Name: "myrepo"}

	actor := auth.Actor{ID: 10, Kind: auth.ActorKindUser, IsSuperAdmin: true}

	mw := gitpkg.PerRepoMutex(locks)

	// The key the middleware should use.
	key := storage.RepoKey{Project: "acme", Type: "git", Repo: "myrepo"}
	mu := locks.For(key)

	var lockHeld bool
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// If the mutex is held by our middleware, TryLock should fail.
		lockHeld = !mu.TryLock()
		if !lockHeld {
			mu.Unlock()
		}
		w.WriteHeader(http.StatusOK)
	})

	r := chi.NewRouter()
	r.Route("/git/{project}/{repo}", func(sub chi.Router) {
		sub.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, rr *http.Request) {
				// Compose: keep chi's route context, layer actor + repo + project on top.
				c := auth.WithActor(rr.Context(), actor)
				c = gitpkg.WithRepo(c, repo)
				c = gitpkg.WithProject(c, "acme")
				next.ServeHTTP(w, rr.WithContext(c))
			})
		})
		sub.Use(mw)
		sub.Handle("/*", inner)
	})

	req := httptest.NewRequest("POST", "/git/acme/myrepo.git/git-receive-pack", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if !lockHeld {
		t.Fatal("per-repo mutex not held during receive-pack")
	}
}

func TestPerRepoMutex_UploadPackNoLock(t *testing.T) {
	locks := storage.NewLocks()
	repo := &metadata.Repo{ID: 1, ProjectID: 42, Name: "myrepo"}

	actor := auth.Actor{ID: 10, Kind: auth.ActorKindUser, IsSuperAdmin: true}

	mw := gitpkg.PerRepoMutex(locks)

	key := storage.RepoKey{Project: "acme", Type: "git", Repo: "myrepo"}
	mu := locks.For(key)

	var lockFree bool
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// For reads, the mutex should NOT be held.
		lockFree = mu.TryLock()
		if lockFree {
			mu.Unlock()
		}
		w.WriteHeader(http.StatusOK)
	})

	r := chi.NewRouter()
	r.Route("/git/{project}/{repo}", func(sub chi.Router) {
		sub.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, rr *http.Request) {
				c := auth.WithActor(rr.Context(), actor)
				c = gitpkg.WithRepo(c, repo)
				c = gitpkg.WithProject(c, "acme")
				next.ServeHTTP(w, rr.WithContext(c))
			})
		})
		sub.Use(mw)
		sub.Handle("/*", inner)
	})

	req := httptest.NewRequest("POST", "/git/acme/myrepo.git/git-upload-pack", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if !lockFree {
		t.Fatal("per-repo mutex held for upload-pack (reads should be lock-free)")
	}
}

// --- Audit middleware smoke test ---

type recordingAudit struct {
	mu      sync.Mutex
	entries []string
}

func (a *recordingAudit) Record(method, path string, status int, bytes int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.entries = append(a.entries, method+" "+path)
}

func TestAuditMiddleware_CapturesRequest(t *testing.T) {
	rec := &recordingAudit{}
	mw := gitpkg.AuditMiddleware(rec)

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("hello"))
	})

	req := httptest.NewRequest("GET", "/git/acme/myrepo.git/info/refs", nil)
	w := httptest.NewRecorder()
	mw(inner).ServeHTTP(w, req)

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.entries) == 0 {
		t.Fatal("no audit entries recorded")
	}
	if !strings.Contains(rec.entries[0], "GET") {
		t.Fatalf("audit entry: %q", rec.entries[0])
	}
}
