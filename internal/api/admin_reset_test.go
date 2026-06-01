package api_test

// admin_reset auth matrix + happy-path tests.
//
// Reuses newTestServer / seedTestUser / s.login / s.do from
// admin_phase1_test.go and withDevEnv from dev_error_routes_test.go (same
// package).

import (
	"context"
	"net/http"
	"testing"
)

// TestAdminReset_DevOff_Returns404 asserts the route is not registered
// when OMNIREPO_DEV is unset — production binaries MUST return 404.
// withDevEnv(t, false) MUST run BEFORE newTestServer(t) because
// mountAdminReset reads the env var at mount time.
func TestAdminReset_DevOff_Returns404(t *testing.T) {
	withDevEnv(t, false)
	s := newTestServer(t)

	resp, _ := s.do(t, "POST", "/api/v1/admin/_reset", "", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("DEV-off status=%d want 404", resp.StatusCode)
	}
}

// TestAdminReset_NoAuth_Returns401 asserts anonymous callers see the
// canonical 401 envelope when DEV mode is on.
func TestAdminReset_NoAuth_Returns401(t *testing.T) {
	withDevEnv(t, true)
	s := newTestServer(t)

	resp, body := s.do(t, "POST", "/api/v1/admin/_reset", "", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anon status=%d want 401; body=%v", resp.StatusCode, body)
	}
	code, _ := body["code"].(string)
	if code != "auth.unauthenticated" {
		t.Errorf("envelope code=%q want auth.unauthenticated", code)
	}
}

// TestAdminReset_NonSuperAdmin_Forbidden asserts a logged-in but
// non-super-admin user receives 403 auth.super_admin_required.
func TestAdminReset_NonSuperAdmin_Forbidden(t *testing.T) {
	withDevEnv(t, true)
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "alice", "a@x", false, false)
	cookie, _, _ := s.login(t, "alice", pw)

	resp, body := s.do(t, "POST", "/api/v1/admin/_reset", cookie, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-super status=%d want 403; body=%v", resp.StatusCode, body)
	}
	code, _ := body["code"].(string)
	if code != "auth.super_admin_required" {
		t.Errorf("envelope code=%q want auth.super_admin_required", code)
	}
}

// TestAdminReset_SuperAdmin_WipesState exercises the happy path: seed a
// project row, POST the reset, assert 200 + {"ok":true}, and verify the
// projects table is empty post-reset while the super-admin users row +
// bootstrap settings survive.
func TestAdminReset_SuperAdmin_WipesState(t *testing.T) {
	withDevEnv(t, true)
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "super", "s@x", true, false)
	cookie, _, _ := s.login(t, "super", pw)

	// Seed bootstrap settings so we can assert they survive. Migration 020
	// already inserts 'maintenance_mode' so OR REPLACE is safe.
	ctx := context.Background()
	for _, kv := range []struct{ k, v string }{
		{"docker_token_hmac_secret", "hmac-test"},
		{"upstream_creds_aead_key", "aead-test"},
	} {
		if _, err := s.db.Writer.ExecContext(ctx,
			`INSERT OR REPLACE INTO settings (key, value) VALUES (?, ?)`, kv.k, kv.v); err != nil {
			t.Fatalf("seed setting %s: %v", kv.k, err)
		}
	}
	// Seed a state row that the reset MUST wipe.
	if _, err := s.db.Writer.ExecContext(ctx,
		`INSERT INTO projects (name) VALUES (?)`, "proj-reset-test"); err != nil {
		t.Fatalf("seed project: %v", err)
	}

	resp, body := s.do(t, "POST", "/api/v1/admin/_reset", cookie, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("super status=%d want 200; body=%v", resp.StatusCode, body)
	}
	ok, _ := body["ok"].(bool)
	if !ok {
		t.Errorf("body[ok]=%v want true (full body=%v)", body["ok"], body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("Content-Type=%q want application/json; charset=utf-8", ct)
	}

	// Projects table must be empty.
	var projectN int
	if err := s.db.Reader.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM projects").Scan(&projectN); err != nil {
		t.Fatalf("count projects: %v", err)
	}
	if projectN != 0 {
		t.Errorf("projects count=%d after reset, want 0", projectN)
	}

	// Super-admin users row must survive.
	var superN int
	if err := s.db.Reader.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM users WHERE is_super_admin = 1").Scan(&superN); err != nil {
		t.Fatalf("count super-admin users: %v", err)
	}
	if superN != 1 {
		t.Errorf("super-admin user count=%d, want 1", superN)
	}

	// Bootstrap settings must survive.
	for _, key := range []string{"docker_token_hmac_secret", "upstream_creds_aead_key"} {
		var v string
		if err := s.db.Reader.QueryRowContext(ctx,
			"SELECT value FROM settings WHERE key = ?", key).Scan(&v); err != nil {
			t.Errorf("bootstrap setting %s missing after reset: %v", key, err)
		}
	}
}
