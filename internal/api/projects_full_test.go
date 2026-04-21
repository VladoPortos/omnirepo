package api_test

import (
	"context"
	"net/http"
	"testing"
)

func TestProjectsFull_ListPaginated(t *testing.T) {
	s := newTestServer(t)
	rootID, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
	cookie, _, code := s.login(t, "root", pw)
	if code != 200 {
		t.Fatalf("login code=%d", code)
	}

	ctx := context.Background()
	for i := 0; i < 3; i++ {
		pid, _ := s.deps.Projects.Create(ctx, "proj-"+string(rune('a'+i)), "")
		_ = s.deps.Members.Add(ctx, pid, rootID)
	}

	resp, body := s.do(t, "GET", "/api/v1/projects?limit=2", cookie, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("code=%d body=%v", resp.StatusCode, body)
	}
	items, ok := body["items"].([]any)
	if !ok {
		t.Fatalf("expected items array, got %v", body)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items with limit=2, got %d", len(items))
	}
	if body["next_cursor"] == "" {
		t.Fatal("expected non-empty next_cursor for pagination")
	}
}

func TestProjectsFull_ListWithMemberCount(t *testing.T) {
	s := newTestServer(t)
	rootID, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
	aliceID, _ := seedTestUser(t, s.db, "alice", "a@x", false, false)
	cookie, _, _ := s.login(t, "root", pw)

	ctx := context.Background()
	pid, _ := s.deps.Projects.Create(ctx, "teamproj", "")
	_ = s.deps.Members.Add(ctx, pid, rootID)
	_ = s.deps.Members.Add(ctx, pid, aliceID)
	_, _ = s.deps.Repos.Create(ctx, pid, "docker", "repo1", "", nil, nil, nil)

	resp, body := s.do(t, "GET", "/api/v1/projects", cookie, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("code=%d", resp.StatusCode)
	}
	items := body["items"].([]any)
	found := false
	for _, raw := range items {
		m := raw.(map[string]any)
		if m["name"] == "teamproj" {
			found = true
			mc := int(m["member_count"].(float64))
			if mc != 2 {
				t.Fatalf("expected member_count=2, got %d", mc)
			}
			rc := int(m["repo_count"].(float64))
			if rc != 1 {
				t.Fatalf("expected repo_count=1, got %d", rc)
			}
		}
	}
	if !found {
		t.Fatal("teamproj not found in list")
	}
}

func TestProjectsFull_GetDetail(t *testing.T) {
	s := newTestServer(t)
	rootID, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
	cookie, _, _ := s.login(t, "root", pw)

	ctx := context.Background()
	pid, _ := s.deps.Projects.Create(ctx, "detail-proj", "some desc")
	_ = s.deps.Members.Add(ctx, pid, rootID)
	_, _ = s.deps.Repos.Create(ctx, pid, "rpm", "centos-repo", "rpm stuff", nil, nil, nil)

	resp, body := s.do(t, "GET", "/api/v1/projects/detail-proj", cookie, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("code=%d body=%v", resp.StatusCode, body)
	}
	if body["name"] != "detail-proj" {
		t.Fatalf("expected name=detail-proj, got %v", body["name"])
	}
	members := body["members"].([]any)
	if len(members) != 1 {
		t.Fatalf("expected 1 member, got %d", len(members))
	}
	repos := body["repos"].([]any)
	if len(repos) != 1 {
		t.Fatalf("expected 1 repo, got %d", len(repos))
	}
}

// TestProjectsFull_ActivityIncludesAPIKeyEvents verifies F-5: the project
// overview Activity widget must include project.api-key.{create,revoke}
// events even though those events target the api-key (not the project) in
// audit_log. The handler reaches into details_json.project to route them
// to the right project.
func TestProjectsFull_ActivityIncludesAPIKeyEvents(t *testing.T) {
	s := newTestServer(t)
	rootID, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
	cookie, _, _ := s.login(t, "root", pw)

	ctx := context.Background()
	pid, _ := s.deps.Projects.Create(ctx, "keyproj", "")
	_ = s.deps.Members.Add(ctx, pid, rootID)

	// Another project's api-key event must NOT leak into keyproj's activity.
	otherPID, _ := s.deps.Projects.Create(ctx, "otherproj", "")
	_ = s.deps.Members.Add(ctx, otherPID, rootID)

	mustInsert := func(kind, targetKind, targetID, projectSlug string) {
		t.Helper()
		if _, err := s.db.Writer.ExecContext(ctx,
			`INSERT INTO audit_log(event_kind, target_kind, target_id, details_json)
			 VALUES (?, ?, ?, ?)`,
			kind, targetKind, targetID,
			`{"project":"`+projectSlug+`","name":"ci-token","prefix":"omr_p_abc"}`,
		); err != nil {
			t.Fatalf("seed audit: %v", err)
		}
	}
	// Matching: project.api-key.create targeting keyproj.
	mustInsert("project.api-key.create", "project_api_key", "42", "keyproj")
	mustInsert("project.api-key.revoke", "project_api_key", "42", "keyproj")
	// Noise: otherproj's api-key event must stay filtered out.
	mustInsert("project.api-key.create", "project_api_key", "99", "otherproj")

	resp, body := s.do(t, "GET", "/api/v1/projects/keyproj/activity", cookie, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("code=%d body=%v", resp.StatusCode, body)
	}
	items := body["items"].([]any)
	var createSeen, revokeSeen, noiseSeen bool
	for _, it := range items {
		m := it.(map[string]any)
		switch m["action"] {
		case "project.api-key.create":
			createSeen = true
			if m["target_id"] == "99" {
				noiseSeen = true
			}
		case "project.api-key.revoke":
			revokeSeen = true
		}
	}
	if !createSeen {
		t.Fatalf("expected keyproj project.api-key.create in activity, got items=%v", items)
	}
	if !revokeSeen {
		t.Fatalf("expected keyproj project.api-key.revoke in activity, got items=%v", items)
	}
	if noiseSeen {
		t.Fatalf("otherproj's api-key event leaked into keyproj activity")
	}
}

func TestProjectsFull_NonMemberDenied(t *testing.T) {
	s := newTestServer(t)
	_, _ = seedTestUser(t, s.db, "root", "r@x", true, false)
	_, alicePW := seedTestUser(t, s.db, "alice", "a@x", false, false)

	ctx := context.Background()
	_, _ = s.deps.Projects.Create(ctx, "secret", "")

	aliceCookie, _, _ := s.login(t, "alice", alicePW)
	resp, _ := s.do(t, "GET", "/api/v1/projects/secret", aliceCookie, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", resp.StatusCode)
	}
}
