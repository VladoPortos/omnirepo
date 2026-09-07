package api_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/vladoportos/omnirepo/internal/metadata"
)

func TestScanVulnerabilitiesPagination(t *testing.T) {
	s := newScanRESTServer(t)
	uid, pw := seedTestUser(t, s.db, "pages", "pages@example.com", true, false)
	cookie, _, _ := s.login(t, "pages", pw)
	_, _, _, rid := seedScanProject(t, s, uid, "docker")
	ctx := context.Background()
	var sid int64
	if err := s.db.WriteTx(ctx, func(tx *sql.Tx) error {
		var err error
		sid, err = metadata.NewScansRepo(s.db).Enqueue(ctx, tx, rid, "docker", "sha256:pages")
		if err != nil {
			return err
		}
		vs := make([]metadata.Vuln, 1003)
		for i := range vs {
			vs[i] = metadata.Vuln{CVEID: fmt.Sprintf("CVE-%04d", i), Severity: "HIGH", PackageName: "p"}
		}
		return metadata.NewVulnerabilitiesRepo(s.db).InsertBatch(ctx, tx, sid, vs, 0)
	}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		query string
		count int
		first string
	}{
		{"?limit=1000&offset=1000", 3, "CVE-1000"},
		{"?limit=2&offset=2", 2, "CVE-0002"},
		{"?limit=1000&offset=1003", 0, ""},
	} {
		req, _ := http.NewRequest("GET", s.ts.URL+fmt.Sprintf("/api/v1/scans/%d/vulnerabilities", sid)+tc.query, nil)
		req.AddCookie(&http.Cookie{Name: "omnirepo_session", Value: cookie})
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var rows []struct {
			CVEID string `json:"cve_id"`
		}
		err = json.NewDecoder(resp.Body).Decode(&rows)
		_ = resp.Body.Close()
		if err != nil || resp.StatusCode != 200 {
			t.Fatalf("status=%d err=%v", resp.StatusCode, err)
		}
		if len(rows) != tc.count {
			t.Fatalf("%s: got %d findings, want %d", tc.query, len(rows), tc.count)
		}
		if len(rows) > 0 && rows[0].CVEID != tc.first {
			t.Fatalf("first=%s want %s", rows[0].CVEID, tc.first)
		}
	}
}
