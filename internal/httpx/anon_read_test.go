package httpx_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dxc-internal/omnirepo/internal/auth"
	"github.com/dxc-internal/omnirepo/internal/httpx"
)

// staticLookup returns fixed public_read / found values.
type staticLookup struct {
	pub   bool
	found bool
}

func (s staticLookup) fn(_ context.Context, _, _, _ string) (bool, bool) {
	return s.pub, s.found
}

// alwaysExtract returns a fixed triple + ok=true regardless of r.
func alwaysExtract(r *http.Request) (string, string, string, bool) {
	return "proj", "raw", "repo", true
}

func neverExtract(r *http.Request) (string, string, string, bool) {
	return "", "", "", false
}

// attachAnon wires the real auth.WithActor so the test asserts the exact
// semantic the production handler wants (Actor{Kind: ActorKindAnonymous}).
func attachAnon(ctx context.Context) context.Context {
	return auth.WithActor(ctx, auth.Actor{Kind: auth.ActorKindAnonymous})
}

func nextAsserts(sawAnon *bool, sawActor *auth.Actor) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a, ok := auth.ActorFromContext(r.Context())
		if ok {
			*sawActor = a
			if a.Kind == auth.ActorKindAnonymous {
				*sawAnon = true
			}
		}
		w.WriteHeader(http.StatusOK)
	}
}

func TestAnonymousReadOK_PublicRepoNoAuth_AttachesAnonymousActor(t *testing.T) {
	var sawAnon bool
	var sawActor auth.Actor
	mw := httpx.AnonymousReadOK(staticLookup{pub: true, found: true}.fn, alwaysExtract, attachAnon)
	h := mw(nextAsserts(&sawAnon, &sawActor))

	req := httptest.NewRequest(http.MethodGet, "/proj/raw/repo/foo.txt", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if !sawAnon {
		t.Fatalf("expected anonymous actor in ctx; got Actor=%+v", sawActor)
	}
	if sawActor.Kind != auth.ActorKindAnonymous {
		t.Fatalf("expected ActorKindAnonymous; got %q", sawActor.Kind)
	}
}

func TestAnonymousReadOK_HEADMethod_AttachesAnonymousActor(t *testing.T) {
	var sawAnon bool
	var sawActor auth.Actor
	mw := httpx.AnonymousReadOK(staticLookup{pub: true, found: true}.fn, alwaysExtract, attachAnon)
	h := mw(nextAsserts(&sawAnon, &sawActor))

	req := httptest.NewRequest(http.MethodHead, "/proj/raw/repo/foo.txt", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if !sawAnon {
		t.Fatalf("HEAD on public repo must attach anonymous actor")
	}
}

func TestAnonymousReadOK_AuthHeaderPresent_DoesNotAttachAnonymous(t *testing.T) {
	var sawAnon bool
	var sawActor auth.Actor
	mw := httpx.AnonymousReadOK(staticLookup{pub: true, found: true}.fn, alwaysExtract, attachAnon)
	h := mw(nextAsserts(&sawAnon, &sawActor))

	req := httptest.NewRequest(http.MethodGet, "/proj/raw/repo/foo.txt", nil)
	req.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if sawAnon {
		t.Fatalf("auth header present: middleware must NOT override to anonymous")
	}
}

func TestAnonymousReadOK_PrivateRepo_FallsThrough(t *testing.T) {
	var sawAnon bool
	var sawActor auth.Actor
	mw := httpx.AnonymousReadOK(staticLookup{pub: false, found: true}.fn, alwaysExtract, attachAnon)
	h := mw(nextAsserts(&sawAnon, &sawActor))

	req := httptest.NewRequest(http.MethodGet, "/proj/raw/repo/foo.txt", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if sawAnon {
		t.Fatalf("private repo: middleware must NOT attach anonymous actor")
	}
}

func TestAnonymousReadOK_RepoNotFound_FallsThrough(t *testing.T) {
	var sawAnon bool
	var sawActor auth.Actor
	mw := httpx.AnonymousReadOK(staticLookup{pub: true, found: false}.fn, alwaysExtract, attachAnon)
	h := mw(nextAsserts(&sawAnon, &sawActor))

	req := httptest.NewRequest(http.MethodGet, "/proj/raw/repo/foo.txt", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if sawAnon {
		t.Fatalf("unknown repo: middleware must NOT attach anonymous actor")
	}
}

func TestAnonymousReadOK_POSTMethod_FallsThrough(t *testing.T) {
	var sawAnon bool
	var sawActor auth.Actor
	mw := httpx.AnonymousReadOK(staticLookup{pub: true, found: true}.fn, alwaysExtract, attachAnon)
	h := mw(nextAsserts(&sawAnon, &sawActor))

	req := httptest.NewRequest(http.MethodPost, "/proj/raw/repo/foo.txt", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if sawAnon {
		t.Fatalf("POST: middleware must NOT attach anonymous actor (writes need real auth)")
	}
}

func TestAnonymousReadOK_PUTMethod_FallsThrough(t *testing.T) {
	var sawAnon bool
	var sawActor auth.Actor
	mw := httpx.AnonymousReadOK(staticLookup{pub: true, found: true}.fn, alwaysExtract, attachAnon)
	h := mw(nextAsserts(&sawAnon, &sawActor))

	req := httptest.NewRequest(http.MethodPut, "/proj/raw/repo/foo.txt", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if sawAnon {
		t.Fatalf("PUT: middleware must NOT attach anonymous actor")
	}
}

func TestAnonymousReadOK_DELETEMethod_FallsThrough(t *testing.T) {
	var sawAnon bool
	var sawActor auth.Actor
	mw := httpx.AnonymousReadOK(staticLookup{pub: true, found: true}.fn, alwaysExtract, attachAnon)
	h := mw(nextAsserts(&sawAnon, &sawActor))

	req := httptest.NewRequest(http.MethodDelete, "/proj/raw/repo/foo.txt", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if sawAnon {
		t.Fatalf("DELETE: middleware must NOT attach anonymous actor")
	}
}

func TestAnonymousReadOK_URLNotRepoScoped_FallsThrough(t *testing.T) {
	var sawAnon bool
	var sawActor auth.Actor
	mw := httpx.AnonymousReadOK(staticLookup{pub: true, found: true}.fn, neverExtract, attachAnon)
	h := mw(nextAsserts(&sawAnon, &sawActor))

	req := httptest.NewRequest(http.MethodGet, "/v2/", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if sawAnon {
		t.Fatalf("non-repo URL: middleware must NOT attach anonymous actor")
	}
}

func TestAnonymousReadOK_NextAlwaysRuns(t *testing.T) {
	called := 0
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called++
		w.WriteHeader(http.StatusOK)
	})
	mw := httpx.AnonymousReadOK(staticLookup{pub: false, found: true}.fn, alwaysExtract, attachAnon)
	h := mw(next)

	req := httptest.NewRequest(http.MethodGet, "/proj/raw/repo/foo.txt", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if called != 1 {
		t.Fatalf("next called %d times; want 1", called)
	}
}
