package api_test

import (
	"context"
	"net/http"
	"testing"
)

func TestDashboard_ReturnsStats(t *testing.T) {
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
	cookie, _, code := s.login(t, "root", pw)
	if code != 200 {
		t.Fatalf("login code=%d", code)
	}

	// Create a project + repo to get non-zero counts.
	ctx := context.Background()
	pid, _ := s.deps.Projects.Create(ctx, "dash-proj", "")
	_, _ = s.deps.Repos.Create(ctx, pid, "docker", "dash-repo", "", nil, nil, nil)

	resp, body := s.do(t, "GET", "/api/v1/dashboard", cookie, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("code=%d body=%v", resp.StatusCode, body)
	}

	repoCount := int(body["repo_count"].(float64))
	if repoCount < 1 {
		t.Fatalf("expected repo_count >= 1, got %d", repoCount)
	}
	userCount := int(body["user_count"].(float64))
	if userCount < 1 {
		t.Fatalf("expected user_count >= 1, got %d", userCount)
	}

	findings, ok := body["scan_findings"].(map[string]any)
	if !ok {
		t.Fatalf("expected scan_findings object, got %v", body["scan_findings"])
	}
	// Should have critical and high keys even if 0.
	if _, ok := findings["critical"]; !ok {
		t.Fatal("expected scan_findings.critical key")
	}
	if _, ok := findings["high"]; !ok {
		t.Fatal("expected scan_findings.high key")
	}
}

func TestDashboard_Unauthenticated(t *testing.T) {
	s := newTestServer(t)
	resp, _ := s.do(t, "GET", "/api/v1/dashboard", "", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}
