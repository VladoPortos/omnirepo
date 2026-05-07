package api_test

// Regression guard for F-03.2 (walkthrough 3 batch 03): user-scope API key
// create and revoke must emit audit events. Before the fix, handleCreateAPIKey
// and handleRevokeAPIKey in internal/api/apikeys.go wrote the DB row but
// never called recordAudit, so an operator querying audit_log for per-user
// key timelines would see nothing. The project-scoped variant
// (project_apikeys.go) did it correctly — this test ensures the user-scope
// handler does the same.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dxc-internal/omnirepo/internal/audit"
)

func TestUserAPIKey_CreateEmitsAudit(t *testing.T) {
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "alice", "alice@x", false, false)
	cookie, _, _ := s.login(t, "alice", pw)

	resp, body := s.do(t, "POST", "/api/v1/me/api-keys", cookie, map[string]any{
		"name": "ci-key",
	})
	if resp.StatusCode != 201 {
		t.Fatalf("create code=%d body=%+v", resp.StatusCode, body)
	}
	secret, _ := body["secret"].(string)
	if secret == "" {
		t.Fatalf("missing secret in create response")
	}

	var detailsJSON, targetKind, targetID string
	err := s.db.Reader.QueryRowContext(context.Background(),
		`SELECT details_json, target_kind, target_id FROM audit_log WHERE event_kind = ?`,
		string(audit.EvtUserAPIKeyCreated)).Scan(&detailsJSON, &targetKind, &targetID)
	if err != nil {
		t.Fatalf("audit query: %v", err)
	}
	if targetKind != "user_api_key" {
		t.Fatalf("target_kind=%q, want user_api_key", targetKind)
	}
	if targetID == "" {
		t.Fatalf("target_id empty; must hold numeric key id for per-key grep")
	}
	var d map[string]any
	if err := json.Unmarshal([]byte(detailsJSON), &d); err != nil {
		t.Fatalf("details_json unmarshal: %v; raw=%s", err, detailsJSON)
	}
	if d["name"] != "ci-key" {
		t.Fatalf("details.name=%v, want ci-key", d["name"])
	}
	if p, ok := d["prefix"].(string); !ok || p == "" {
		t.Fatalf("details.prefix missing or empty: %+v", d)
	}
	if strings.Contains(detailsJSON, secret) {
		t.Fatalf("audit details_json leaks plaintext secret: %s", detailsJSON)
	}
}

// F-03.3 (wt3): names > 128 chars must be rejected so the profile table
// layout and audit NDJSON stay predictable.
func TestUserAPIKey_RejectsOverlongName(t *testing.T) {
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "alice", "alice@x", false, false)
	cookie, _, _ := s.login(t, "alice", pw)

	longName := strings.Repeat("x", 129)
	resp, body := s.do(t, "POST", "/api/v1/me/api-keys", cookie, map[string]any{
		"name": longName,
	})
	if resp.StatusCode != 422 {
		t.Fatalf("want 422 for overlong name, got %d body=%+v", resp.StatusCode, body)
	}
	if body["code"] != "validation.failed" {
		t.Fatalf("want validation.failed code, got %v", body["code"])
	}
}

// F-03.4 (wt3): duplicate names against the live keyset must be rejected
// so the UI table can identify a key by name. Revoked names are reusable.
func TestUserAPIKey_RejectsDuplicateLiveName(t *testing.T) {
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "alice", "alice@x", false, false)
	cookie, _, _ := s.login(t, "alice", pw)

	if r, _ := s.do(t, "POST", "/api/v1/me/api-keys", cookie, map[string]any{
		"name": "dup",
	}); r.StatusCode != 201 {
		t.Fatalf("first create code=%d", r.StatusCode)
	}
	resp, body := s.do(t, "POST", "/api/v1/me/api-keys", cookie, map[string]any{
		"name": "dup",
	})
	if resp.StatusCode != 409 {
		t.Fatalf("want 409 on duplicate name, got %d body=%+v", resp.StatusCode, body)
	}

	// Revoking the first one frees the name.
	resp, body = s.do(t, "GET", "/api/v1/me/api-keys", cookie, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("list code=%d body=%+v", resp.StatusCode, body)
	}
	var items []map[string]any
	if arr, ok := body["items"].([]any); ok { // defensive — s.do wraps non-object responses
		for _, v := range arr {
			if m, ok := v.(map[string]any); ok {
				items = append(items, m)
			}
		}
	}
	if len(items) == 0 {
		// Fallback: the list endpoint actually returns a bare array.
		// s.do wraps bare arrays under "items" in the test helper only for admin
		// responses — hit it with a raw client to be robust.
		items = append(items, listAPIKeys(t, s, cookie)...)
	}
	if len(items) == 0 {
		t.Fatalf("expected at least one key in list")
	}
	firstID := idStr(items[0]["id"])
	if r, _ := s.do(t, "DELETE", "/api/v1/me/api-keys/"+firstID, cookie, nil); r.StatusCode != 204 {
		t.Fatalf("revoke code=%d", r.StatusCode)
	}
	if r, _ := s.do(t, "POST", "/api/v1/me/api-keys", cookie, map[string]any{
		"name": "dup",
	}); r.StatusCode != 201 {
		t.Fatalf("want 201 after revoking duplicate, got %d", r.StatusCode)
	}
}

func listAPIKeys(t *testing.T, s *testServer, cookie string) []map[string]any {
	t.Helper()
	resp, buf := s.doBytes(t, "GET", "/api/v1/me/api-keys", cookie, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("list code=%d body=%s", resp.StatusCode, buf)
	}
	var raw []map[string]any
	if err := json.Unmarshal(buf, &raw); err != nil {
		t.Fatalf("list unmarshal: %v; body=%s", err, buf)
	}
	return raw
}

func TestUserAPIKey_RevokeEmitsAudit(t *testing.T) {
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "alice", "alice@x", false, false)
	cookie, _, _ := s.login(t, "alice", pw)

	resp, body := s.do(t, "POST", "/api/v1/me/api-keys", cookie, map[string]any{
		"name": "revoke-me",
	})
	if resp.StatusCode != 201 {
		t.Fatalf("create code=%d body=%+v", resp.StatusCode, body)
	}
	id := idStr(body["id"])

	resp, _ = s.do(t, "DELETE", "/api/v1/me/api-keys/"+id, cookie, nil)
	if resp.StatusCode != 204 {
		t.Fatalf("revoke code=%d", resp.StatusCode)
	}

	var n int
	if err := s.db.Reader.QueryRowContext(context.Background(),
		`SELECT count(*) FROM audit_log WHERE event_kind = ? AND target_id = ?`,
		string(audit.EvtUserAPIKeyRevoked), id).Scan(&n); err != nil {
		t.Fatalf("audit count query: %v", err)
	}
	if n != 1 {
		t.Fatalf("revoke audit rows = %d, want 1", n)
	}
}
