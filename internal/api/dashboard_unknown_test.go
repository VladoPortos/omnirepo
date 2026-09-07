package api_test

import (
	"context"
	"testing"
)

func TestDashboardCountsUnknownFindings(t *testing.T) {
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
	cookie, _, _ := s.login(t, "root", pw)
	ctx := context.Background()
	pid, err := s.deps.Projects.Create(ctx, "unknown", "")
	if err != nil {
		t.Fatal(err)
	}
	rid, err := s.deps.Repos.Create(ctx, pid, "raw", "files", "", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.db.Writer.Exec(`INSERT INTO scans(repo_id,artifact_kind,artifact_id,status) VALUES (?,'raw','file','done')`, rid)
	if err != nil {
		t.Fatal(err)
	}
	sid, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	for _, severity := range []string{"UNKNOWN", "FUTURE"} {
		if _, err := s.db.Writer.Exec(`INSERT INTO vulnerabilities(scan_id,cve_id,severity,package_name) VALUES (?,'finding',?,'pkg')`, sid, severity); err != nil {
			t.Fatal(err)
		}
	}
	resp, body := s.do(t, "GET", "/api/v1/dashboard", cookie, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d body=%v", resp.StatusCode, body)
	}
	findings, ok := body["scan_findings"].(map[string]any)
	if !ok || findings["unknown"] != float64(2) {
		t.Fatalf("unknown findings missing: %v", body["scan_findings"])
	}
}
