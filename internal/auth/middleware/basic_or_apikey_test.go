package middleware_test

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dxc-internal/omnirepo/internal/auth"
	"github.com/dxc-internal/omnirepo/internal/auth/middleware"
)

func basicAuthHeader(login, password string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(login+":"+password))
}

func TestBasicPasswordSuccess(t *testing.T) {
	e := newEnv(t)
	h := middleware.BasicOrAPIKey(e.Deps)(okHandler())
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", basicAuthHeader("alice", e.AlicePwPlain))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d body=%q", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "login=alice") {
		t.Fatalf("body: %q", w.Body.String())
	}
}

func TestBasicPasswordWrongReturns401(t *testing.T) {
	e := newEnv(t)
	h := middleware.BasicOrAPIKey(e.Deps)(okHandler())
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", basicAuthHeader("alice", "wrong"))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status: %d", w.Code)
	}
}

func TestBasicUnknownLoginReturns401(t *testing.T) {
	e := newEnv(t)
	h := middleware.BasicOrAPIKey(e.Deps)(okHandler())
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", basicAuthHeader("nobody", "swordfish"))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status: %d", w.Code)
	}
}

func TestBasicAPIKeyAsPasswordSuccess(t *testing.T) {
	// KEY-06: Basic <anything>:<omr_...> authenticates via API key.
	e := newEnv(t)
	h := middleware.BasicOrAPIKey(e.Deps)(okHandler())
	req := httptest.NewRequest("GET", "/", nil)
	// Use a nonsense login; API-key path ignores it.
	req.Header.Set("Authorization", basicAuthHeader("docker", e.AliceAPIKey.Plaintext))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d body=%q", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "login=alice") {
		t.Fatalf("body: %q", w.Body.String())
	}
}

func TestBasicAPIKeyAsPasswordUpdatesLastUsedAt(t *testing.T) {
	e := newEnv(t)
	h := middleware.BasicOrAPIKey(e.Deps)(okHandler())
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", basicAuthHeader("whoever", e.AliceAPIKey.Plaintext))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d", w.Code)
	}
	k, err := e.APIKeys.FindByPrefixSha(context.Background(), e.AliceAPIKey.Prefix, e.AliceAPIKey.SHA256)
	if err != nil {
		t.Fatalf("post-lookup: %v", err)
	}
	if k.LastUsedAt == nil {
		t.Fatalf("LastUsedAt nil after Basic-APIKey auth")
	}
}

func TestBasicNoAuthHeaderReturns401(t *testing.T) {
	e := newEnv(t)
	h := middleware.BasicOrAPIKey(e.Deps)(okHandler())
	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status: %d", w.Code)
	}
}

func TestBasicMCPUserPassesMiddlewareButCanDenies(t *testing.T) {
	e := newEnv(t)
	var captured auth.Actor
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured, _ = auth.ActorFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	h := middleware.BasicOrAPIKey(e.Deps)(inner)
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", basicAuthHeader("carol", e.CarolPwPlain))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d body=%q", w.Code, w.Body.String())
	}
	if !captured.MustChangePassword {
		t.Fatalf("captured actor MCP=false; want true")
	}
	allowed, reason := auth.Can(req.Context(), captured, auth.ActionCreateProject, auth.Target{})
	if allowed || reason != auth.ReasonPasswordChangeRequired {
		t.Fatalf("Can: %v/%q; want false/%s",
			allowed, reason, auth.ReasonPasswordChangeRequired)
	}
}

func TestRequireCanWith_ResolvesTarget(t *testing.T) {
	e := newEnv(t)
	// Alice (non-MCP, non-super-admin). ActionChangeOwnPassword with matching target.UserID should pass.
	chain := middleware.BasicOrAPIKey(e.Deps)(
		middleware.RequireCanWith(auth.ActionChangeOwnPassword, func(r *http.Request) auth.Target {
			return auth.Target{UserID: e.AliceID}
		})(okHandler()),
	)
	req := httptest.NewRequest("POST", "/", nil)
	req.Header.Set("Authorization", basicAuthHeader("alice", e.AlicePwPlain))
	w := httptest.NewRecorder()
	chain.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d, body=%q", w.Code, w.Body.String())
	}
}
