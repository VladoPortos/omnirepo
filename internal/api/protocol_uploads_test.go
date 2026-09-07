package api_test

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vladoportos/omnirepo/internal/api"
	"github.com/vladoportos/omnirepo/internal/auth"
	"github.com/vladoportos/omnirepo/internal/metadata"
	"github.com/vladoportos/omnirepo/internal/protocol/raw"
	"github.com/vladoportos/omnirepo/internal/storage"
)

func TestRAWUploadRESTSessionPermissionsAndRollback(t *testing.T) {
	s := newTestServer(t, func(d *api.Deps) {
		root := filepath.Join(d.DataRoot, "repos")
		d.ProtocolUploads = &api.ProtocolUploadsDeps{RAW: raw.New(raw.Deps{
			DB: d.DB, Users: d.Users, Sessions: d.Sessions, APIKeys: d.APIKeys,
			Projects: d.Projects, Repos: d.Repos, Members: d.Members,
			Files: metadata.NewRawFilesRepo(d.DB), Scans: metadata.NewScansRepo(d.DB),
			Path: storage.NewPathStore(root), RepoRoot: root, Audit: d.Audit, MaxPutBytes: 32,
		})}
	})
	ctx := context.Background()
	pid, err := s.deps.Projects.Create(ctx, "upload-proj", "")
	if err != nil {
		t.Fatal(err)
	}
	autoScan := false
	if _, err := s.deps.Repos.Create(ctx, pid, "raw", "files", "", &autoScan, nil, nil); err != nil {
		t.Fatal(err)
	}
	cookies := map[string]string{}
	for _, role := range []string{"maintainer", "viewer", "outsider"} {
		uid, pw := seedTestUser(t, s.db, role, role+"@example.test", false, false)
		if role != "outsider" {
			if _, err := s.db.Writer.Exec(`INSERT INTO project_members(project_id,user_id,role) VALUES (?,?,?)`, pid, uid, role); err != nil {
				t.Fatal(err)
			}
		}
		cookie, _, status := s.login(t, role, pw)
		if status != 200 {
			t.Fatalf("login %s: %d", role, status)
		}
		cookies[role] = cookie
	}
	upload := func(cookie, body string) int {
		t.Helper()
		req, err := http.NewRequest("PUT", s.ts.URL+"/api/v1/projects/upload-proj/repos/raw/files/artifacts/folder/file%20%25%23.txt", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if cookie != "" {
			req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: cookie})
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.StatusCode
	}
	for _, denied := range []struct {
		cookie string
		status int
	}{{"", 401}, {cookies["viewer"], 403}, {cookies["outsider"], 403}} {
		if got := upload(denied.cookie, "denied"); got != denied.status {
			t.Fatalf("denied upload: %d want %d", got, denied.status)
		}
	}
	if got := upload(cookies["maintainer"], "original"); got != 201 {
		t.Fatalf("session upload: %d", got)
	}
	path := filepath.Join(s.dataRoot, "repos", "upload-proj", "raw", "files", "folder", "file %#.txt")
	if body, err := os.ReadFile(path); err != nil || string(body) != "original" {
		t.Fatalf("uploaded bytes: %q %v", body, err)
	}
	if _, err := s.db.Writer.Exec(`CREATE TRIGGER fail_raw_upload BEFORE INSERT ON raw_files BEGIN SELECT RAISE(ABORT, 'failpoint'); END`); err != nil {
		t.Fatal(err)
	}
	if got := upload(cookies["maintainer"], "replacement"); got != 500 {
		t.Fatalf("failed metadata write: %d", got)
	}
	if body, err := os.ReadFile(path); err != nil || string(body) != "original" {
		t.Fatalf("previous payload lost: %q %v", body, err)
	}
	if got := upload(cookies["maintainer"], strings.Repeat("x", 33)); got != 413 {
		t.Fatalf("size limit: %d", got)
	}
}
