package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/vladoportos/omnirepo/internal/auth"
)

// TestAdminJSONBodyCap pins audit finding #10: admin JSON endpoints that
// previously decoded r.Body unbounded now reject bodies above the shared cap
// with 413 instead of buffering arbitrary amounts.
func TestAdminJSONBodyCap(t *testing.T) {
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
	cookie, _, _ := s.login(t, "root", pw)

	// Build a JSON body bigger than 64 KiB. Pad with a very long
	// description_md so the decoded struct still parses in principle.
	big := strings.Repeat("A", 128*1024)
	body, _ := json.Marshal(map[string]any{
		"name":           "pX",
		"description_md": big,
	})
	req, _ := http.NewRequest("POST", s.ts.URL+"/api/v1/projects", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: cookie})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("want 413, got code=%d body=%s", resp.StatusCode, b)
	}
}

// TestProjectScopedAPIKey_SeesItsProject pins audit finding #8: a project-owned
// API key (ProjectScope set, user ID 0) must be able to see its project on
// /api/v1/projects and get scoped counts from /api/v1/dashboard. Before the
// fix these handlers gated visibility on Members.ListProjectIDsForUser(0),
// which returned nothing — so project keys authenticated but saw no data.
func TestProjectScopedAPIKey_SeesItsProject(t *testing.T) {
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
	cookie, _, _ := s.login(t, "root", pw)

	s.do(t, "POST", "/api/v1/projects", cookie, map[string]any{"name": "proj"})

	var pid int64
	if err := s.db.Reader.QueryRowContext(context.Background(),
		`SELECT id FROM projects WHERE name='proj'`).Scan(&pid); err != nil {
		t.Fatalf("find project id: %v", err)
	}

	// Create a project-scoped API key directly in the DB.
	k, err := auth.GenerateAPIKey(auth.APIKeyKindProject)
	if err != nil {
		t.Fatalf("gen project key: %v", err)
	}
	if _, err := s.deps.APIKeys.CreateProjectKey(context.Background(), pid, "ci-key", k.Prefix, k.SHA256); err != nil {
		t.Fatalf("persist project key: %v", err)
	}

	// REST endpoints auth via Authorization: Bearer <omr_p_...> (the Basic
	// project:<proj>:<key> form is for the protocol-tree middleware).
	bearer := "Bearer " + k.Plaintext
	doAs := func(path string) map[string]any {
		t.Helper()
		req, _ := http.NewRequest("GET", s.ts.URL+path, nil)
		req.Header.Set("Authorization", bearer)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			b, _ := io.ReadAll(resp.Body)
			t.Fatalf("GET %s code=%d body=%s", path, resp.StatusCode, b)
		}
		var body map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&body)
		return body
	}

	// Projects list: the key's project must appear.
	list := doAs("/api/v1/projects")
	items, _ := list["items"].([]any)
	if len(items) == 0 {
		t.Fatalf("project-scoped key saw no projects: %+v", list)
	}
	found := false
	for _, it := range items {
		m, _ := it.(map[string]any)
		if m["name"] == "proj" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("project-scoped key did not see its own project: %+v", items)
	}

	// Dashboard: must NOT short-circuit to zeros because "no memberships".
	// The handler should include the project in its scope clause via
	// ProjectScope. We can't assert counts without generating artifacts,
	// but we can assert the request succeeds and returns the expected
	// top-level keys rather than a zeroed-out response caused by an empty
	// visible-project set.
	dash := doAs("/api/v1/dashboard")
	if dash == nil {
		t.Fatal("dashboard returned nil body")
	}
	if _, ok := dash["recent_activity"]; !ok {
		t.Fatalf("dashboard response missing expected key: %+v", dash)
	}
}
