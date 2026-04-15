package middleware_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dxc-internal/omnirepo/internal/auth"
	"github.com/dxc-internal/omnirepo/internal/auth/middleware"
)

func TestSessionOrAPIKey_NoCredentialsReturns401(t *testing.T) {
	e := newEnv(t)
	h := middleware.SessionOrAPIKey(e.Deps)(okHandler())
	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status: %d, want 401", w.Code)
	}
	if !strings.Contains(w.Body.String(), "unauthenticated") {
		t.Fatalf("body: %q", w.Body.String())
	}
}

func TestSessionCookieAuthSuccess(t *testing.T) {
	e := newEnv(t)
	h := middleware.SessionOrAPIKey(e.Deps)(okHandler())
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: e.AliceSessionTok})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d, body=%q", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "login=alice") {
		t.Fatalf("body: %q", w.Body.String())
	}
}

func TestSessionRevokedReturns401(t *testing.T) {
	e := newEnv(t)
	if err := e.Sessions.Delete(context.Background(), e.AliceSessionID); err != nil {
		t.Fatalf("delete session: %v", err)
	}
	h := middleware.SessionOrAPIKey(e.Deps)(okHandler())
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: e.AliceSessionTok})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status: %d", w.Code)
	}
}

func TestBearerAPIKeySuccess(t *testing.T) {
	e := newEnv(t)
	h := middleware.SessionOrAPIKey(e.Deps)(okHandler())
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+e.AliceAPIKey.Plaintext)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d, body=%q", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "login=alice") {
		t.Fatalf("body: %q", w.Body.String())
	}
}

func TestBearerAPIKeyUpdatesLastUsedAt(t *testing.T) {
	e := newEnv(t)
	// Pre-check: LastUsedAt is nil on the freshly-seeded key.
	before, err := e.APIKeys.FindByPrefixSha(context.Background(), e.AliceAPIKey.Prefix, e.AliceAPIKey.SHA256)
	if err != nil {
		t.Fatalf("pre-lookup: %v", err)
	}
	if before.LastUsedAt != nil {
		t.Fatalf("seed LastUsedAt: want nil, got %v", before.LastUsedAt)
	}
	h := middleware.SessionOrAPIKey(e.Deps)(okHandler())
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+e.AliceAPIKey.Plaintext)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d", w.Code)
	}
	after, err := e.APIKeys.FindByPrefixSha(context.Background(), e.AliceAPIKey.Prefix, e.AliceAPIKey.SHA256)
	if err != nil {
		t.Fatalf("post-lookup: %v", err)
	}
	if after.LastUsedAt == nil {
		t.Fatalf("LastUsedAt still nil after auth; TouchLastUsed not called")
	}
}

func TestInvalidBearerFormatRejected(t *testing.T) {
	e := newEnv(t)
	h := middleware.SessionOrAPIKey(e.Deps)(okHandler())
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer not-a-key")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status: %d", w.Code)
	}
}

func TestMalformedCookieReturns401(t *testing.T) {
	e := newEnv(t)
	h := middleware.SessionOrAPIKey(e.Deps)(okHandler())
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "x"})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status: %d", w.Code)
	}
}

func TestMCPUserPassesMiddlewareButCanDenies(t *testing.T) {
	e := newEnv(t)
	// Issue a session for Carol (MCP user).
	s, err := auth.GenerateSession()
	if err != nil {
		t.Fatalf("gen session: %v", err)
	}
	if _, err := e.Sessions.Create(context.Background(), e.CarolID, s.Prefix, s.SHA256,
		e.Deps.Clock(), e.Deps.Clock().Add(24*3600*1_000_000_000)); err != nil {
		t.Fatalf("seed carol session: %v", err)
	}
	var capturedActor auth.Actor
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a, _ := auth.ActorFromContext(r.Context())
		capturedActor = a
		w.WriteHeader(http.StatusOK)
	})
	h := middleware.SessionOrAPIKey(e.Deps)(inner)
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: s.Plaintext})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("middleware should identify MCP user, status=%d", w.Code)
	}
	if !capturedActor.MustChangePassword || capturedActor.Login != "carol" {
		t.Fatalf("captured actor: %+v", capturedActor)
	}
	allowed, reason := auth.Can(req.Context(), capturedActor, auth.ActionCreateRepo, auth.Target{ProjectID: 1})
	if allowed || reason != auth.ReasonPasswordChangeRequired {
		t.Fatalf("Can MCP actor CreateRepo: %v/%q; want false/%s",
			allowed, reason, auth.ReasonPasswordChangeRequired)
	}
}

func TestRequireCanReturns403PasswordChangeRequired(t *testing.T) {
	e := newEnv(t)
	// Carol session.
	s, err := auth.GenerateSession()
	if err != nil {
		t.Fatalf("gen: %v", err)
	}
	if _, err := e.Sessions.Create(context.Background(), e.CarolID, s.Prefix, s.SHA256,
		e.Deps.Clock(), e.Deps.Clock().Add(24*3600*1_000_000_000)); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Chain: SessionOrAPIKey → RequireCan(ActionCreateRepo) → okHandler
	chain := middleware.SessionOrAPIKey(e.Deps)(
		middleware.RequireCan(auth.ActionCreateRepo)(okHandler()),
	)
	req := httptest.NewRequest("POST", "/", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: s.Plaintext})
	w := httptest.NewRecorder()
	chain.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status: %d, body=%q; want 403", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), auth.ReasonPasswordChangeRequired) {
		t.Fatalf("body does not mention password-change-required: %q", w.Body.String())
	}
}
