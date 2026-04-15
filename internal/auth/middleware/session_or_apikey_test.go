package middleware_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
		time.Now().UTC(), time.Now().Add(24*time.Hour).UTC()); err != nil {
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

// TestSlidingSessionExpiryExtendsOnActivity is the WR-02 regression gate:
// a fresh-minted session that is about to expire in a few minutes must be
// extended forward by session_ttl when authenticated again — but never past
// the issued_at + hard_cap bound.
func TestSlidingSessionExpiryExtendsOnActivity(t *testing.T) {
	e := newEnv(t)

	// Freeze middleware clock at t0 = now.
	t0 := time.Now().UTC().Truncate(time.Second)
	e.Deps.Clock = func() time.Time { return t0 }
	e.Deps.SessionTTL = 12 * time.Hour
	e.Deps.SessionHardTTL = 7 * 24 * time.Hour

	// Issue a session with issued=t0-11h, expires=t0+1h (already nearly at
	// the TTL boundary). Authenticating should slide expires forward.
	s, err := auth.GenerateSession()
	if err != nil {
		t.Fatalf("gen: %v", err)
	}
	issued := t0.Add(-11 * time.Hour)
	oldExpires := t0.Add(1 * time.Hour)
	sid, err := e.Sessions.Create(context.Background(), e.AliceID, s.Prefix, s.SHA256, issued, oldExpires)
	if err != nil {
		t.Fatalf("seed session: %v", err)
	}

	h := middleware.SessionOrAPIKey(e.Deps)(okHandler())
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: s.Plaintext})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d, body=%q", w.Code, w.Body.String())
	}

	// expires_at must have been pushed forward.
	row, err := e.Sessions.FindByPrefixSha(context.Background(), s.Prefix, s.SHA256)
	if err != nil {
		t.Fatalf("reload session: %v", err)
	}
	_ = sid
	// Should be min(t0+12h, issued+7d). issued = t0-11h → issued+7d =
	// t0 + 6d13h which is > t0+12h, so cap is t0+12h.
	wantExpires := t0.Add(12 * time.Hour)
	if !row.ExpiresAt.Truncate(time.Second).Equal(wantExpires) {
		t.Fatalf("slid expires_at: got %v want %v (old was %v)",
			row.ExpiresAt, wantExpires, oldExpires)
	}
}

// TestHardCapRejectsSessionPastSevenDays is the WR-02 hard-cap regression
// gate: a session that is still "active" (expires_at in the future due to
// regular touches) must still be rejected once now > issued_at + 7d.
func TestHardCapRejectsSessionPastSevenDays(t *testing.T) {
	e := newEnv(t)

	t0 := time.Now().UTC().Truncate(time.Second)
	e.Deps.Clock = func() time.Time { return t0 }
	e.Deps.SessionTTL = 12 * time.Hour
	e.Deps.SessionHardTTL = 7 * 24 * time.Hour

	// Session issued 8 days ago with expires_at 10 hours in the future
	// (imagine it had been sliding-touched every ~11h). Hard cap should
	// reject regardless.
	s, err := auth.GenerateSession()
	if err != nil {
		t.Fatalf("gen: %v", err)
	}
	issued := t0.Add(-8 * 24 * time.Hour)
	futureExpires := t0.Add(10 * time.Hour)
	if _, err := e.Sessions.Create(context.Background(), e.AliceID, s.Prefix, s.SHA256, issued, futureExpires); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	h := middleware.SessionOrAPIKey(e.Deps)(okHandler())
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: s.Plaintext})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("past-hard-cap session should 401, got %d body=%q", w.Code, w.Body.String())
	}
}

// TestSlidingSessionCappedAtHardTTL verifies that once issued_at + hard_cap
// is within session_ttl of now, the slide clamps to the hard cap rather
// than walking past it.
func TestSlidingSessionCappedAtHardTTL(t *testing.T) {
	e := newEnv(t)

	t0 := time.Now().UTC().Truncate(time.Second)
	e.Deps.Clock = func() time.Time { return t0 }
	e.Deps.SessionTTL = 12 * time.Hour
	e.Deps.SessionHardTTL = 7 * 24 * time.Hour

	// Session issued 6d20h ago; touching at t0 would otherwise extend
	// expires_at to t0+12h, but the hard cap is issued+7d = t0+4h.
	issued := t0.Add(-(6*24 + 20) * time.Hour)
	s, err := auth.GenerateSession()
	if err != nil {
		t.Fatalf("gen: %v", err)
	}
	oldExpires := t0.Add(1 * time.Hour)
	if _, err := e.Sessions.Create(context.Background(), e.AliceID, s.Prefix, s.SHA256, issued, oldExpires); err != nil {
		t.Fatalf("seed: %v", err)
	}

	h := middleware.SessionOrAPIKey(e.Deps)(okHandler())
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: s.Plaintext})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d", w.Code)
	}

	row, err := e.Sessions.FindByPrefixSha(context.Background(), s.Prefix, s.SHA256)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	wantCap := issued.Add(7 * 24 * time.Hour)
	if !row.ExpiresAt.Truncate(time.Second).Equal(wantCap) {
		t.Fatalf("clamped expires_at: got %v want %v", row.ExpiresAt, wantCap)
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
		time.Now().UTC(), time.Now().Add(24*time.Hour).UTC()); err != nil {
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
