package httpx_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dxc-internal/omnirepo/internal/httpx"
	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/metadata/sqlitetest"
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

	// The toggle route must remain reachable so operators can disable
	// maintenance mode once it has been enabled.
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch} {
		req := httptest.NewRequest(method, "/api/v1/admin/maintenance", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s /api/v1/admin/maintenance: expected 200 (toggle bypass), got %d", method, rec.Code)
		}
	}

	// Any other admin write path must still be blocked.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/gc/run", nil)
	rec := httptest.NewRecorder()
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
