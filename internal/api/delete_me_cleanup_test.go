package api_test

// Regression guard for F-03.6 (walkthrough 3 batch 03 Codex pass): DELETE
// /api/v1/me soft-deletes the user, which means the FK cascade on sessions
// and api_keys never fires. Without explicit cleanup the sessions table
// keeps rows tied to a now-dead login, and api_keys keeps the live-name
// partial unique index's slot occupied — both are inert under the current
// middleware (which rejects soft-deleted users), but that invariant
// becomes load-bearing if nothing explicitly drains them.
//
// Assert the handler drops every session row and revokes every live key
// before returning 200.

import (
	"context"
	"testing"
)

func TestDeleteMe_DropsSessionsAndRevokesAPIKeys(t *testing.T) {
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "alice", "a@x", false, false)
	cookie, _, _ := s.login(t, "alice", pw)

	// Create two keys so we exercise the "more than one row" path.
	for _, name := range []string{"laptop", "ci"} {
		if r, _ := s.do(t, "POST", "/api/v1/me/api-keys", cookie, map[string]any{"name": name}); r.StatusCode != 201 {
			t.Fatalf("create %s code=%d", name, r.StatusCode)
		}
	}
	// Baseline: 1 session + 2 live keys.
	var sessionCount, liveKeys int
	if err := s.db.Reader.QueryRowContext(context.Background(),
		`SELECT count(*) FROM sessions WHERE user_id=(SELECT id FROM users WHERE login='alice')`).Scan(&sessionCount); err != nil {
		t.Fatal(err)
	}
	if err := s.db.Reader.QueryRowContext(context.Background(),
		`SELECT count(*) FROM api_keys WHERE owner_user_id=(SELECT id FROM users WHERE login='alice') AND revoked_at IS NULL`).Scan(&liveKeys); err != nil {
		t.Fatal(err)
	}
	if sessionCount != 1 || liveKeys != 2 {
		t.Fatalf("pre-delete: sessions=%d liveKeys=%d; want 1/2", sessionCount, liveKeys)
	}

	// Delete the account.
	if r, _ := s.do(t, "DELETE", "/api/v1/me", cookie, nil); r.StatusCode != 200 {
		t.Fatalf("delete code=%d", r.StatusCode)
	}

	// Post-state: 0 sessions, 0 live keys (everything revoked).
	if err := s.db.Reader.QueryRowContext(context.Background(),
		`SELECT count(*) FROM sessions WHERE user_id=(SELECT id FROM users WHERE login='alice')`).Scan(&sessionCount); err != nil {
		t.Fatal(err)
	}
	if err := s.db.Reader.QueryRowContext(context.Background(),
		`SELECT count(*) FROM api_keys WHERE owner_user_id=(SELECT id FROM users WHERE login='alice') AND revoked_at IS NULL`).Scan(&liveKeys); err != nil {
		t.Fatal(err)
	}
	if sessionCount != 0 {
		t.Fatalf("post-delete sessions=%d, want 0", sessionCount)
	}
	if liveKeys != 0 {
		t.Fatalf("post-delete liveKeys=%d, want 0", liveKeys)
	}
}
