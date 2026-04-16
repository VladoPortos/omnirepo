package api_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/dxc-internal/omnirepo/internal/audit"
)

func TestAdminAudit_List(t *testing.T) {
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
	cookie, _, code := s.login(t, "root", pw)
	if code != 200 {
		t.Fatalf("login code=%d", code)
	}

	// Seed a few audit events of different kinds.
	for _, kind := range []audit.EventKind{audit.EvtUserCreated, audit.EvtProjectCreated, audit.EvtRepoCreated} {
		uid := int64(1)
		if err := s.deps.Audit.Record(context.Background(), audit.Event{
			Kind:        kind,
			ActorUserID: &uid,
			TargetKind:  "test",
			TargetID:    "t1",
			Outcome:     "ok",
		}); err != nil {
			t.Fatal(err)
		}
	}

	// List all.
	resp, body := s.do(t, "GET", "/api/v1/admin/audit?limit=10", cookie, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("code=%d body=%v", resp.StatusCode, body)
	}
	items, ok := body["items"].([]any)
	if !ok || len(items) == 0 {
		t.Fatalf("expected items, got %v", body)
	}
}

func TestAdminAudit_FilterByAction(t *testing.T) {
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
	cookie, _, _ := s.login(t, "root", pw)

	// Seed specific events.
	uid := int64(1)
	_ = s.deps.Audit.Record(context.Background(), audit.Event{
		Kind: audit.EvtUserCreated, ActorUserID: &uid, TargetKind: "user", TargetID: "bob",
	})
	_ = s.deps.Audit.Record(context.Background(), audit.Event{
		Kind: audit.EvtProjectCreated, ActorUserID: &uid, TargetKind: "project", TargetID: "p1",
	})

	resp, body := s.do(t, "GET", "/api/v1/admin/audit?action=user.created", cookie, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("code=%d", resp.StatusCode)
	}
	items := body["items"].([]any)
	for _, raw := range items {
		m := raw.(map[string]any)
		if m["action"] != "user.created" {
			t.Fatalf("unexpected action=%v", m["action"])
		}
	}
}

func TestAdminAudit_CursorPagination(t *testing.T) {
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
	cookie, _, _ := s.login(t, "root", pw)

	// Seed enough events for pagination.
	uid := int64(1)
	for i := 0; i < 5; i++ {
		_ = s.deps.Audit.Record(context.Background(), audit.Event{
			Kind: audit.EvtUserCreated, ActorUserID: &uid, TargetKind: "user", TargetID: "u",
			OccurredAt: time.Now().UTC().Add(time.Duration(i) * time.Second),
		})
	}

	// Page 1: limit=2
	resp, body := s.do(t, "GET", "/api/v1/admin/audit?limit=2", cookie, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("code=%d", resp.StatusCode)
	}
	items := body["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	cursor, ok := body["next_cursor"].(string)
	if !ok || cursor == "" {
		t.Fatalf("expected next_cursor, got %v", body["next_cursor"])
	}

	// Page 2: use cursor
	resp2, body2 := s.do(t, "GET", "/api/v1/admin/audit?limit=2&cursor="+cursor, cookie, nil)
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("page2 code=%d", resp2.StatusCode)
	}
	items2 := body2["items"].([]any)
	if len(items2) != 2 {
		t.Fatalf("expected 2 items on page2, got %d", len(items2))
	}
}

func TestAdminAudit_NonSuperAdmin403(t *testing.T) {
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "alice", "a@x", false, false)
	cookie, _, _ := s.login(t, "alice", pw)

	resp, _ := s.do(t, "GET", "/api/v1/admin/audit", cookie, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("code=%d want 403", resp.StatusCode)
	}
}
