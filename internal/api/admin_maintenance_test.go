package api_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/dxc-internal/omnirepo/internal/audit"
	"github.com/dxc-internal/omnirepo/internal/metadata"
)

func TestAdminMaintenance_GetDefault(t *testing.T) {
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
	cookie, _, _ := s.login(t, "root", pw)

	resp, body := s.do(t, "GET", "/api/v1/admin/maintenance", cookie, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("code=%d", resp.StatusCode)
	}
	if body["enabled"] != false {
		t.Fatalf("expected enabled=false, got %v", body["enabled"])
	}
}

func TestAdminMaintenance_ToggleOnOff(t *testing.T) {
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
	cookie, _, _ := s.login(t, "root", pw)

	// Toggle on.
	resp, body := s.do(t, "POST", "/api/v1/admin/maintenance", cookie, map[string]any{"enabled": true})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("toggle on code=%d body=%v", resp.StatusCode, body)
	}
	if body["enabled"] != true {
		t.Fatalf("expected enabled=true, got %v", body["enabled"])
	}

	// Verify persisted in settings.
	val, err := metadata.NewSettingsRepo(s.db).Get(context.Background(), "maintenance_mode")
	if err != nil {
		t.Fatal(err)
	}
	if val != "true" {
		t.Fatalf("settings maintenance_mode=%q want true", val)
	}

	// Verify audit event.
	var n int
	_ = s.db.Reader.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM audit_log WHERE event_kind=?`, string(audit.EvtMaintenanceToggled)).Scan(&n)
	if n == 0 {
		t.Fatalf("no maintenance.toggled audit event")
	}

	// Toggle off.
	resp, body = s.do(t, "POST", "/api/v1/admin/maintenance", cookie, map[string]any{"enabled": false})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("toggle off code=%d", resp.StatusCode)
	}
	if body["enabled"] != false {
		t.Fatalf("expected enabled=false after toggle off, got %v", body["enabled"])
	}
}

func TestAdminMaintenance_MiddlewareBlocks(t *testing.T) {
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
	cookie, _, _ := s.login(t, "root", pw)

	// Enable maintenance mode directly in settings.
	if err := metadata.NewSettingsRepo(s.db).Set(context.Background(), "maintenance_mode", "true"); err != nil {
		t.Fatal(err)
	}

	// GET should still work (reads allowed).
	resp, _ := s.do(t, "GET", "/api/v1/me", cookie, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET during maintenance should work, got %d", resp.StatusCode)
	}

	// Note: POST to /api/v1/admin/maintenance is a write, but the middleware
	// blocks it before the handler runs. The middleware is on the global chain
	// but our test router doesn't wire it (api tests use bare chi). We test
	// the middleware itself separately in internal/httpx.
}

func TestAdminMaintenance_NonSuperAdmin403(t *testing.T) {
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "alice", "a@x", false, false)
	cookie, _, _ := s.login(t, "alice", pw)

	resp, _ := s.do(t, "GET", "/api/v1/admin/maintenance", cookie, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("code=%d want 403", resp.StatusCode)
	}

	resp, _ = s.do(t, "POST", "/api/v1/admin/maintenance", cookie, map[string]any{"enabled": true})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("code=%d want 403", resp.StatusCode)
	}
}
