package httpx_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vladoportos/omnirepo/internal/httpx"
	"github.com/vladoportos/omnirepo/internal/metadata"
	"github.com/vladoportos/omnirepo/internal/metadata/sqlitetest"
)

func TestMaintenanceMode_PassThroughWhenDisabled(t *testing.T) {
	db := sqlitetest.New(t)
	settings := metadata.NewSettingsRepo(db)

	handler := httpx.MaintenanceMode(settings)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/test", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestMaintenanceMode_BlocksWritesWhenEnabled(t *testing.T) {
	db := sqlitetest.New(t)
	settings := metadata.NewSettingsRepo(db)
	if err := settings.Set(context.Background(), "maintenance_mode", "true"); err != nil {
		t.Fatal(err)
	}

	handler := httpx.MaintenanceMode(settings)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		req := httptest.NewRequest(method, "/test", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s: expected 503, got %d", method, rec.Code)
		}
		if rec.Header().Get("Retry-After") != "300" {
			t.Fatalf("%s: missing Retry-After header", method)
		}
	}
}

func TestMaintenanceMode_AllowsReadsWhenEnabled(t *testing.T) {
	db := sqlitetest.New(t)
	settings := metadata.NewSettingsRepo(db)
	if err := settings.Set(context.Background(), "maintenance_mode", "true"); err != nil {
		t.Fatal(err)
	}

	handler := httpx.MaintenanceMode(settings)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodOptions} {
		req := httptest.NewRequest(method, "/test", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("%s: expected 200, got %d", method, rec.Code)
		}
	}
}

func TestMaintenanceMode_ToggleRoutePassThroughWhenEnabled(t *testing.T) {
	db := sqlitetest.New(t)
	settings := metadata.NewSettingsRepo(db)
	if err := settings.Set(context.Background(), "maintenance_mode", "true"); err != nil {
		t.Fatal(err)
	}

	handler := httpx.MaintenanceMode(settings)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// The toggle route must remain reachable on POST so operators can
	// disable maintenance mode once it has been enabled.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/maintenance", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/v1/admin/maintenance: expected 200 (toggle bypass), got %d", rec.Code)
	}

	// Other write methods on the toggle route stay gated — the handler
	// only registers POST + GET, so PUT/PATCH would 405 from chi anyway,
	// but the middleware should not silently widen the bypass.
	for _, method := range []string{http.MethodPut, http.MethodPatch, http.MethodDelete} {
		req := httptest.NewRequest(method, "/api/v1/admin/maintenance", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s /api/v1/admin/maintenance: expected 503 (bypass is POST-only), got %d", method, rec.Code)
		}
	}

	// Any other admin write path must still be blocked.
	req = httptest.NewRequest(http.MethodPost, "/api/v1/admin/gc/run", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected non-toggle admin writes to stay blocked, got %d", rec.Code)
	}
}

func TestMaintenanceMode_NilSettingsPassThrough(t *testing.T) {
	handler := httpx.MaintenanceMode(nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/test", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestMaintenanceAllowsAuthenticationAndGitReadsOnly(t *testing.T) {
	settings := metadata.NewSettingsRepo(sqlitetest.New(t))
	if err := settings.Set(context.Background(), "maintenance_mode", "true"); err != nil {
		t.Fatal(err)
	}
	h := httpx.MaintenanceMode(settings)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{"POST", "/api/v1/auth/login", 204},
		{"POST", "/api/v1/auth/logout", 204},
		{"POST", "/api/v1/auth/change-password", 204},
		{"POST", "/team/git/repo.git/git-upload-pack", 204},
		{"POST", "/git/team/repo.git/git-upload-pack", 204},
		{"POST", "/team/git/repo.git/git-receive-pack", 503},
		{"PUT", "/team/raw/repo/git-upload-pack", 503},
		{"POST", "/team/raw/repo/git-upload-pack", 503},
		{"POST", "/api/v1/admin/gc", 503},
		{"POST", "/api/v1/setup/superadmin", 503},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
			if w.Code != tc.want {
				t.Fatalf("status=%d want=%d", w.Code, tc.want)
			}
		})
	}
}
