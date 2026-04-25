package api_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dxc-internal/omnirepo/internal/api"
	"github.com/dxc-internal/omnirepo/internal/metadata"
)

// --------------------------------------------------------------------------
// Plan 02-11: PATCH + wipe endpoints (REPO-05, REPO-07, REPO-09).
// --------------------------------------------------------------------------

// bootProjectAndRepo creates a project with super-admin and a repo of the
// given type, returning the super-admin cookie and project name.
func bootProjectAndRepo(t *testing.T, s *testServer, proj, repoType, repoName string) string {
	t.Helper()
	seedTestUser(t, s.db, "super", "s@x", true, false)
	cookie, _, _ := s.login(t, "super", "pw-super")
	if resp, body := s.do(t, "POST", "/api/v1/projects", cookie, api.CreateProjectRequest{Name: proj}); resp.StatusCode != 200 {
		t.Fatalf("create project: %d %+v", resp.StatusCode, body)
	}
	if resp, body := s.do(t, "POST", "/api/v1/projects/"+proj+"/repos", cookie, api.CreateRepoRequest{Name: repoName, Type: repoType}); resp.StatusCode != 200 {
		t.Fatalf("create repo: %d %+v", resp.StatusCode, body)
	}
	return cookie
}

func TestRepoPatch_PartialOnlyAutoScan(t *testing.T) {
	s := newTestServer(t)
	cookie := bootProjectAndRepo(t, s, "pr", "docker", "r1")

	autoScan := false
	resp, body := s.do(t, "PATCH", "/api/v1/projects/pr/repos/docker/r1", cookie,
		map[string]any{"auto_scan": autoScan})
	if resp.StatusCode != 200 {
		t.Fatalf("patch code=%d body=%+v", resp.StatusCode, body)
	}
	if v, ok := body["auto_scan"].(bool); !ok || v != false {
		t.Fatalf("auto_scan not updated: body=%+v", body)
	}
	// default block_on_severity and public_read untouched.
	if v, ok := body["block_on_severity"].(string); !ok || v != "none" {
		t.Fatalf("block_on_severity unexpected: %+v", body)
	}
	if v, ok := body["public_read"].(bool); !ok || v {
		t.Fatalf("public_read should still be false: %+v", body)
	}
	// Audit row for repo.updated with diff containing auto_scan.
	var details string
	err := s.db.Reader.QueryRowContext(context.Background(), `
		SELECT details_json FROM audit_log WHERE event_kind='repo.updated' ORDER BY id DESC LIMIT 1
	`).Scan(&details)
	if err != nil {
		t.Fatalf("no repo.updated audit row: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(details), &parsed); err != nil {
		t.Fatalf("details_json not json: %v (%s)", err, details)
	}
	diff, ok := parsed["diff"].(map[string]any)
	if !ok {
		t.Fatalf("missing diff in audit details: %s", details)
	}
	if _, has := diff["auto_scan"]; !has {
		t.Fatalf("diff missing auto_scan: %+v", diff)
	}
	if _, has := diff["description_md"]; has {
		t.Fatalf("diff should NOT contain description_md when unchanged: %+v", diff)
	}
}

func TestRepoPatch_InvalidBlockOnSeverity(t *testing.T) {
	s := newTestServer(t)
	cookie := bootProjectAndRepo(t, s, "pr", "docker", "r1")
	resp, _ := s.do(t, "PATCH", "/api/v1/projects/pr/repos/docker/r1", cookie,
		map[string]any{"block_on_severity": "bogus"})
	if resp.StatusCode != 400 && resp.StatusCode != 422 {
		t.Fatalf("expected 400/422, got %d", resp.StatusCode)
	}
}

func TestRepoPatch_PublicReadFlip(t *testing.T) {
	s := newTestServer(t)
	cookie := bootProjectAndRepo(t, s, "pr", "raw", "r1")
	resp, body := s.do(t, "PATCH", "/api/v1/projects/pr/repos/raw/r1", cookie,
		map[string]any{"public_read": true})
	if resp.StatusCode != 200 {
		t.Fatalf("code=%d body=%+v", resp.StatusCode, body)
	}
	if v, ok := body["public_read"].(bool); !ok || !v {
		t.Fatalf("public_read not true: %+v", body)
	}
}

func TestRepoPatch_NotFound(t *testing.T) {
	s := newTestServer(t)
	cookie := bootProjectAndRepo(t, s, "pr", "docker", "r1")
	resp, _ := s.do(t, "PATCH", "/api/v1/projects/pr/repos/docker/no-such", cookie,
		map[string]any{"auto_scan": true})
	if resp.StatusCode != 404 {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestRepoPatch_NonMember403(t *testing.T) {
	s := newTestServer(t)
	// super creates project+repo.
	bootProjectAndRepo(t, s, "pr", "docker", "r1")
	// Separate non-member user.
	seedTestUser(t, s.db, "mallory", "m@x", false, false)
	cookie, _, _ := s.login(t, "mallory", "pw-mallory")
	resp, _ := s.do(t, "PATCH", "/api/v1/projects/pr/repos/docker/r1", cookie,
		map[string]any{"auto_scan": true})
	if resp.StatusCode != 403 {
		t.Fatalf("expected 403 non-member, got %d", resp.StatusCode)
	}
}

func TestRepoWipe_RawMovesDirToTrash(t *testing.T) {
	s := newTestServer(t)
	cookie := bootProjectAndRepo(t, s, "pr", "raw", "r1")

	// Seed raw_files rows + on-disk tree.
	ctx := context.Background()
	p, err := s.deps.Projects.FindByName(ctx, "pr")
	if err != nil {
		t.Fatal(err)
	}
	rr, err := s.deps.Repos.FindByTriple(ctx, p.ID, "raw", "r1")
	if err != nil {
		t.Fatal(err)
	}
	if err := writeRawSeed(s, rr.ID, 5); err != nil {
		t.Fatal(err)
	}
	// On-disk repo dir.
	onDisk := filepath.Join(s.dataRoot, "repos", "pr", "raw", "r1")
	if err := os.MkdirAll(onDisk, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(onDisk, "a.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	resp, body := s.do(t, "POST", "/api/v1/projects/pr/repos/raw/r1/wipe", cookie, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("wipe code=%d body=%+v", resp.StatusCode, body)
	}
	if v, ok := body["artifact_count"].(float64); !ok || int(v) != 5 {
		t.Fatalf("artifact_count=%v", body["artifact_count"])
	}
	// raw_files rows gone.
	files, err := metadata.NewRawFilesRepo(s.db).ListDir(ctx, rr.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Fatalf("raw_files not emptied: %d remain", len(files))
	}
	// Dir moved to trash.
	if _, err := os.Stat(onDisk); !os.IsNotExist(err) {
		t.Fatalf("expected on-disk gone, got err=%v", err)
	}
	trash, _ := os.ReadDir(filepath.Join(s.dataRoot, "trash"))
	if len(trash) == 0 {
		t.Fatalf("expected trash entry")
	}
	// Audit row.
	var n int
	_ = s.db.Reader.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM audit_log WHERE event_kind='repo.wiped'`).Scan(&n)
	if n == 0 {
		t.Fatalf("no repo.wiped audit row")
	}
}

func TestRepoWipe_DockerSharedBlobsSurvive(t *testing.T) {
	s := newTestServer(t)
	cookie := bootProjectAndRepo(t, s, "pr", "docker", "r1")
	// Add a sibling repo r2 that shares a blob with r1.
	s.do(t, "POST", "/api/v1/projects/pr/repos", cookie, api.CreateRepoRequest{Name: "r2", Type: "docker"})

	ctx := context.Background()
	p, _ := s.deps.Projects.FindByName(ctx, "pr")
	r1, _ := s.deps.Repos.FindByTriple(ctx, p.ID, "docker", "r1")
	r2, _ := s.deps.Repos.FindByTriple(ctx, p.ID, "docker", "r2")
	blobsRepo := metadata.NewDockerBlobsRepo(s.db)
	mfRepo := metadata.NewDockerManifestsRepo(s.db)
	tagsRepo := metadata.NewDockerTagsRepo(s.db)

	shared := "sha256:shared"
	err := writeDockerSeed(s, blobsRepo, mfRepo, tagsRepo, r1.ID, r2.ID, shared)
	if err != nil {
		t.Fatal(err)
	}

	resp, body := s.do(t, "POST", "/api/v1/projects/pr/repos/docker/r1/wipe", cookie, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("wipe code=%d body=%+v", resp.StatusCode, body)
	}
	// Shared blob still has ref_count 1 from r2.
	b, _ := blobsRepo.Stat(ctx, shared)
	if b == nil || b.RefCount != 1 {
		t.Fatalf("shared blob refcount after wipe: %+v", b)
	}
	// r1 manifests gone; r2 manifests survive.
	if m, _ := mfRepo.GetByDigest(ctx, r1.ID, "sha256:m1"); m != nil {
		t.Fatalf("r1 manifest should be gone")
	}
	if m, _ := mfRepo.GetByDigest(ctx, r2.ID, "sha256:m2"); m == nil {
		t.Fatalf("r2 manifest must survive")
	}
}

func TestRepoWipe_UnsupportedType501(t *testing.T) {
	s := newTestServer(t)
	cookie := bootProjectAndRepo(t, s, "pr", "helm", "h1")
	resp, _ := s.do(t, "POST", "/api/v1/projects/pr/repos/helm/h1/wipe", cookie, nil)
	if resp.StatusCode != 501 {
		t.Fatalf("expected 501 for helm wipe in Phase 2, got %d", resp.StatusCode)
	}
}

func TestRepoWipe_NonMember403(t *testing.T) {
	s := newTestServer(t)
	bootProjectAndRepo(t, s, "pr", "raw", "r1")
	seedTestUser(t, s.db, "mallory", "m@x", false, false)
	cookie, _, _ := s.login(t, "mallory", "pw-mallory")
	resp, _ := s.do(t, "POST", "/api/v1/projects/pr/repos/raw/r1/wipe", cookie, nil)
	if resp.StatusCode != 403 {
		t.Fatalf("expected 403, got %d", resp.StatusCode)
	}
}

func TestRepoWipe_NotFound404(t *testing.T) {
	s := newTestServer(t)
	cookie := bootProjectAndRepo(t, s, "pr", "raw", "r1")
	resp, _ := s.do(t, "POST", "/api/v1/projects/pr/repos/raw/ghost/wipe", cookie, nil)
	if resp.StatusCode != 404 {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

// TestCreateRepo_InvalidName_ErrorSaysRepoNotProject locks the fix for the
// phase-6 walkthrough copy bug: the repo-create validator error must refer
// to the *repo* name, not the project name. The UI echoes this wire message
// verbatim in the validation envelope; reusing `ProjectNameValid` leaked
// "invalid project name ..." into the Create Repository dialog.
func TestCreateRepo_InvalidName_ErrorSaysRepoNotProject(t *testing.T) {
	s := newTestServer(t)
	seedTestUser(t, s.db, "super", "s@x", true, false)
	cookie, _, _ := s.login(t, "super", "pw-super")
	if resp, body := s.do(t, "POST", "/api/v1/projects", cookie,
		api.CreateProjectRequest{Name: "proj"}); resp.StatusCode != 200 {
		t.Fatalf("create project: %d %+v", resp.StatusCode, body)
	}
	resp, body := s.do(t, "POST", "/api/v1/projects/proj/repos", cookie,
		api.CreateRepoRequest{Name: "BAD NAME!!", Type: "docker"})
	if resp.StatusCode != 422 {
		t.Fatalf("bad repo name code=%d body=%+v, want 422", resp.StatusCode, body)
	}
	msg, _ := body["message"].(string)
	if !strings.Contains(msg, "invalid repo name") {
		t.Fatalf("error message should mention 'invalid repo name', got %q", msg)
	}
	if strings.Contains(msg, "invalid project name") {
		t.Fatalf("error message must not mention 'invalid project name', got %q", msg)
	}
	// Envelope must also carry details.field: "name" so the UI can
	// highlight the Repository Name <Input> on the dialog — the second
	// half of the phase-6 walkthrough ERR-06 fix.
	details, _ := body["details"].(map[string]any)
	if field, _ := details["field"].(string); field != "name" {
		t.Fatalf("envelope should carry details.field=\"name\", got %+v", body)
	}
}

// ---- helpers ----

func writeRawSeed(s *testServer, repoID int64, n int) error {
	raw := metadata.NewRawFilesRepo(s.db)
	ctx := context.Background()
	return s.db.WriteTx(ctx, func(tx *sql.Tx) error {
		for i := 0; i < n; i++ {
			if err := raw.Insert(ctx, tx, repoID, seedPath(i), int64(10), "text/plain", "hex"); err != nil {
				return err
			}
		}
		return nil
	})
}

func writeDockerSeed(s *testServer, blobs *metadata.DockerBlobsRepo, mans *metadata.DockerManifestsRepo, tags *metadata.DockerTagsRepo, r1id, r2id int64, shared string) error {
	ctx := context.Background()
	return s.db.WriteTx(ctx, func(tx *sql.Tx) error {
		if err := blobs.UpsertZeroRef(ctx, tx, shared, 500); err != nil {
			return err
		}
		body1 := []byte(`{"schemaVersion":2,"config":{"digest":"` + shared + `"},"layers":[]}`)
		if err := mans.Insert(ctx, tx, r1id, "sha256:m1", "application/vnd.oci.image.manifest.v1+json", body1); err != nil {
			return err
		}
		if err := blobs.IncRef(ctx, tx, shared); err != nil {
			return err
		}
		if _, err := tags.Upsert(ctx, tx, r1id, "", "latest", "sha256:m1"); err != nil {
			return err
		}
		body2 := []byte(`{"schemaVersion":2,"config":{"digest":"` + shared + `"},"layers":[]}`)
		if err := mans.Insert(ctx, tx, r2id, "sha256:m2", "application/vnd.oci.image.manifest.v1+json", body2); err != nil {
			return err
		}
		if err := blobs.IncRef(ctx, tx, shared); err != nil {
			return err
		}
		if _, err := tags.Upsert(ctx, tx, r2id, "", "latest", "sha256:m2"); err != nil {
			return err
		}
		return nil
	})
}

func seedPath(i int) string {
	return "f-" + itoaShort(i) + ".txt"
}

func itoaShort(i int) string {
	if i == 0 {
		return "0"
	}
	var b [12]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(b[pos:])
}

// --------------------------------------------------------------------------
// Phase 8 Plan 01 (MIRROR-01..07) — CreateRepo + PatchRepo mirror validation.
// --------------------------------------------------------------------------

// bootProjectOnly is bootProjectAndRepo without the repo. Used by mirror
// tests that post a CreateRepoRequest with is_mirror=true and want to
// assert on the handler response, not a pre-seeded repo row.
func bootProjectOnly(t *testing.T, s *testServer, proj string) string {
	t.Helper()
	seedTestUser(t, s.db, "super", "s@x", true, false)
	cookie, _, _ := s.login(t, "super", "pw-super")
	if resp, body := s.do(t, "POST", "/api/v1/projects", cookie, api.CreateProjectRequest{Name: proj}); resp.StatusCode != 200 {
		t.Fatalf("create project: %d %+v", resp.StatusCode, body)
	}
	return cookie
}

func TestCreateRepo_MirrorRejectsUnsupportedType(t *testing.T) {
	s := newTestServer(t)
	cookie := bootProjectOnly(t, s, "p-mirror")
	resp, body := s.do(t, "POST", "/api/v1/projects/p-mirror/repos", cookie, map[string]any{
		"name":                "r1",
		"type":                "raw",
		"is_mirror":           true,
		"mirror_upstream_url": "https://archive.example/raw",
	})
	if resp.StatusCode != 400 {
		t.Fatalf("status = %d, want 400; body=%+v", resp.StatusCode, body)
	}
	if code, _ := body["code"].(string); !strings.Contains(code, "mirror_type_unsupported") {
		t.Fatalf("code = %q, want *mirror_type_unsupported; body=%+v", code, body)
	}
}

func TestCreateRepo_MirrorRejectsBadURL(t *testing.T) {
	s := newTestServer(t)
	cookie := bootProjectOnly(t, s, "p-mirror")
	resp, body := s.do(t, "POST", "/api/v1/projects/p-mirror/repos", cookie, map[string]any{
		"name":                "r1",
		"type":                "deb",
		"is_mirror":           true,
		"mirror_upstream_url": "file:///etc/passwd",
	})
	if resp.StatusCode != 400 {
		t.Fatalf("status = %d, want 400; body=%+v", resp.StatusCode, body)
	}
	if code, _ := body["code"].(string); !strings.Contains(code, "mirror_url_invalid") {
		t.Fatalf("code = %q, want *mirror_url_invalid; body=%+v", code, body)
	}
}

func TestCreateRepo_MirrorRejectsBadFilter(t *testing.T) {
	s := newTestServer(t)
	cookie := bootProjectOnly(t, s, "p-mirror")
	resp, body := s.do(t, "POST", "/api/v1/projects/p-mirror/repos", cookie, map[string]any{
		"name":                "r1",
		"type":                "deb",
		"is_mirror":           true,
		"mirror_upstream_url": "https://archive.ubuntu.com/ubuntu",
		"mirror_filter":       map[string]any{"NotAKey": true},
	})
	if resp.StatusCode != 400 {
		t.Fatalf("status = %d, want 400; body=%+v", resp.StatusCode, body)
	}
	if code, _ := body["code"].(string); !strings.Contains(code, "mirror_filter_invalid") {
		t.Fatalf("code = %q, want *mirror_filter_invalid; body=%+v", code, body)
	}
}

func TestCreateRepo_MirrorRejectsCrossProjectCred(t *testing.T) {
	s := newTestServerWithUpstream(t)
	// Boot two projects with the super-admin.
	seedTestUser(t, s.db, "super", "s@x", true, false)
	cookie, _, _ := s.login(t, "super", "pw-super")
	if resp, body := s.do(t, "POST", "/api/v1/projects", cookie, api.CreateProjectRequest{Name: "proj-a"}); resp.StatusCode != 200 {
		t.Fatalf("create project a: %d %+v", resp.StatusCode, body)
	}
	if resp, body := s.do(t, "POST", "/api/v1/projects", cookie, api.CreateProjectRequest{Name: "proj-b"}); resp.StatusCode != 200 {
		t.Fatalf("create project b: %d %+v", resp.StatusCode, body)
	}
	// Create a cred in proj-b directly via the repo API to get its id.
	var projBID int64
	_ = s.db.Reader.QueryRowContext(context.Background(), `SELECT id FROM projects WHERE name='proj-b'`).Scan(&projBID)
	credID, err := s.deps.UpstreamCreds.Create(context.Background(), projBID, "archive.example", metadata.CredKindAPT, "u", "pw", "", 0)
	if err != nil {
		t.Fatalf("seed cred in proj-b: %v", err)
	}
	// Try to create a mirror repo in proj-a that references proj-b's cred.
	resp, body := s.do(t, "POST", "/api/v1/projects/proj-a/repos", cookie, map[string]any{
		"name":                "r1",
		"type":                "deb",
		"is_mirror":           true,
		"mirror_upstream_url": "https://archive.example/ubuntu",
		"mirror_cred_id":      credID,
	})
	if resp.StatusCode != 400 {
		t.Fatalf("status = %d, want 400; body=%+v", resp.StatusCode, body)
	}
	if code, _ := body["code"].(string); !strings.Contains(code, "mirror_cred_wrong_project") {
		t.Fatalf("code = %q, want *mirror_cred_wrong_project; body=%+v", code, body)
	}
}

func TestCreateRepo_MirrorHappyPath(t *testing.T) {
	s := newTestServer(t)
	cookie := bootProjectOnly(t, s, "p-mirror")
	resp, body := s.do(t, "POST", "/api/v1/projects/p-mirror/repos", cookie, map[string]any{
		"name":                "ubuntu-focal",
		"type":                "deb",
		"is_mirror":           true,
		"mirror_upstream_url": "https://archive.ubuntu.com/ubuntu",
		"mirror_filter":       map[string]any{"Suites": []string{"focal"}, "Components": []string{"main"}},
		"scan_on_sync":        true,
	})
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200; body=%+v", resp.StatusCode, body)
	}
	// Read the row to assert the 5 mirror columns were persisted.
	ctx := context.Background()
	var pid int64
	_ = s.db.Reader.QueryRowContext(ctx, `SELECT id FROM projects WHERE name='p-mirror'`).Scan(&pid)
	rr, err := s.deps.Repos.FindByTriple(ctx, pid, "deb", "ubuntu-focal")
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if !rr.IsMirror {
		t.Errorf("IsMirror = false")
	}
	if rr.MirrorUpstreamURL != "https://archive.ubuntu.com/ubuntu" {
		t.Errorf("URL = %q", rr.MirrorUpstreamURL)
	}
	if !strings.Contains(rr.MirrorFilterJSON, "Suites") {
		t.Errorf("filter missing Suites: %q", rr.MirrorFilterJSON)
	}
	if !rr.ScanOnSync {
		t.Errorf("ScanOnSync = false")
	}
}

func TestPatchRepo_RejectsIsMirrorEdit(t *testing.T) {
	s := newTestServer(t)
	cookie := bootProjectAndRepo(t, s, "pr", "deb", "r1")
	want := true
	resp, body := s.do(t, "PATCH", "/api/v1/projects/pr/repos/deb/r1", cookie, map[string]any{
		"is_mirror": want,
	})
	if resp.StatusCode != 400 {
		t.Fatalf("status = %d, want 400; body=%+v", resp.StatusCode, body)
	}
	if code, _ := body["code"].(string); !strings.Contains(code, "mirror_url_immutable") {
		t.Fatalf("code = %q; body=%+v", code, body)
	}
}

func TestPatchRepo_RejectsURLEdit(t *testing.T) {
	s := newTestServer(t)
	cookie := bootProjectAndRepo(t, s, "pr", "deb", "r1")
	resp, body := s.do(t, "PATCH", "/api/v1/projects/pr/repos/deb/r1", cookie, map[string]any{
		"mirror_upstream_url": "https://mutated.example/ubuntu",
	})
	if resp.StatusCode != 400 {
		t.Fatalf("status = %d, want 400; body=%+v", resp.StatusCode, body)
	}
	if code, _ := body["code"].(string); !strings.Contains(code, "mirror_url_immutable") {
		t.Fatalf("code = %q; body=%+v", code, body)
	}
}

func TestPatchRepo_RejectsCrossProjectCred(t *testing.T) {
	s := newTestServerWithUpstream(t)
	seedTestUser(t, s.db, "super", "s@x", true, false)
	cookie, _, _ := s.login(t, "super", "pw-super")
	// Two projects + a mirror repo in proj-a.
	if resp, _ := s.do(t, "POST", "/api/v1/projects", cookie, api.CreateProjectRequest{Name: "proj-a"}); resp.StatusCode != 200 {
		t.Fatal("create proj-a")
	}
	if resp, _ := s.do(t, "POST", "/api/v1/projects", cookie, api.CreateProjectRequest{Name: "proj-b"}); resp.StatusCode != 200 {
		t.Fatal("create proj-b")
	}
	if resp, body := s.do(t, "POST", "/api/v1/projects/proj-a/repos", cookie, map[string]any{
		"name":                "r1",
		"type":                "deb",
		"is_mirror":           true,
		"mirror_upstream_url": "https://archive.example/ubuntu",
	}); resp.StatusCode != 200 {
		t.Fatalf("create mirror repo: %d %+v", resp.StatusCode, body)
	}
	// Cred in proj-b.
	ctx := context.Background()
	var projBID int64
	_ = s.db.Reader.QueryRowContext(ctx, `SELECT id FROM projects WHERE name='proj-b'`).Scan(&projBID)
	credID, err := s.deps.UpstreamCreds.Create(ctx, projBID, "archive.example", metadata.CredKindAPT, "u", "pw", "", 0)
	if err != nil {
		t.Fatalf("seed cred: %v", err)
	}
	resp, body := s.do(t, "PATCH", "/api/v1/projects/proj-a/repos/deb/r1", cookie, map[string]any{
		"mirror_cred_id": credID,
	})
	if resp.StatusCode != 400 {
		t.Fatalf("status = %d, want 400; body=%+v", resp.StatusCode, body)
	}
	if code, _ := body["code"].(string); !strings.Contains(code, "mirror_cred_wrong_project") {
		t.Fatalf("code = %q; body=%+v", code, body)
	}
}

// --------------------------------------------------------------------------
// Phase 06-02 (DRIFTPURGE-04, D-17) — drift_purge PATCH + GET coverage.
// --------------------------------------------------------------------------

// TestHandlePatchRepo_DriftPurgeMirrorOnly locks the mirror-only invariant:
// PATCH'ing drift_purge=true against a non-mirror repo must 400 with the
// codeRepoDriftPurgeMirrorOnly envelope code. The non-mirror repo here is
// the default repo created via bootProjectAndRepo (deb without is_mirror).
func TestHandlePatchRepo_DriftPurgeMirrorOnly(t *testing.T) {
	s := newTestServer(t)
	cookie := bootProjectAndRepo(t, s, "pr", "deb", "r1")

	resp, body := s.do(t, "PATCH", "/api/v1/projects/pr/repos/deb/r1", cookie,
		map[string]any{"drift_purge": true})
	if resp.StatusCode != 400 {
		t.Fatalf("status = %d, want 400; body=%+v", resp.StatusCode, body)
	}
	code, _ := body["code"].(string)
	if code != "repo.drift_purge_mirror_only" {
		t.Fatalf("envelope code = %q, want repo.drift_purge_mirror_only; body=%+v", code, body)
	}
}

// TestHandlePatchRepo_DriftPurgeOnMirror_Accepted exercises the happy path:
// PATCH drift_purge=true on a mirror repo returns 200 and GET reflects the
// flag. PATCH drift_purge=false flips it back; GET shows false.
func TestHandlePatchRepo_DriftPurgeOnMirror_Accepted(t *testing.T) {
	s := newTestServer(t)
	cookie := bootProjectOnly(t, s, "pr")
	if resp, body := s.do(t, "POST", "/api/v1/projects/pr/repos", cookie, map[string]any{
		"name":                "mirror-r1",
		"type":                "deb",
		"is_mirror":           true,
		"mirror_upstream_url": "https://archive.example/ubuntu",
	}); resp.StatusCode != 200 {
		t.Fatalf("create mirror repo: %d %+v", resp.StatusCode, body)
	}

	// Default drift_purge is false on creation (migration 035 DEFAULT 0).
	resp, body := s.do(t, "GET", "/api/v1/projects/pr/repos/deb/mirror-r1", cookie, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("GET pre-patch status = %d; body=%+v", resp.StatusCode, body)
	}
	if v, ok := body["drift_purge"].(bool); !ok || v {
		t.Fatalf("GET pre-patch drift_purge = %v, want false; body=%+v", body["drift_purge"], body)
	}

	// PATCH drift_purge=true.
	resp, body = s.do(t, "PATCH", "/api/v1/projects/pr/repos/deb/mirror-r1", cookie,
		map[string]any{"drift_purge": true})
	if resp.StatusCode != 200 {
		t.Fatalf("patch true status = %d; body=%+v", resp.StatusCode, body)
	}
	if v, ok := body["drift_purge"].(bool); !ok || !v {
		t.Fatalf("PATCH response drift_purge = %v, want true; body=%+v", body["drift_purge"], body)
	}

	// GET round-trips the new value.
	resp, body = s.do(t, "GET", "/api/v1/projects/pr/repos/deb/mirror-r1", cookie, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("GET post-patch status = %d; body=%+v", resp.StatusCode, body)
	}
	if v, ok := body["drift_purge"].(bool); !ok || !v {
		t.Fatalf("GET post-patch drift_purge = %v, want true; body=%+v", body["drift_purge"], body)
	}

	// Audit diff captures the change for plan 06-07 consumption.
	var details string
	err := s.db.Reader.QueryRowContext(context.Background(), `
		SELECT details_json FROM audit_log WHERE event_kind='repo.updated' ORDER BY id DESC LIMIT 1
	`).Scan(&details)
	if err != nil {
		t.Fatalf("no repo.updated audit row: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(details), &parsed); err != nil {
		t.Fatalf("details_json not json: %v (%s)", err, details)
	}
	diff, ok := parsed["diff"].(map[string]any)
	if !ok {
		t.Fatalf("missing diff in audit details: %s", details)
	}
	dp, ok := diff["drift_purge"].(map[string]any)
	if !ok {
		t.Fatalf("diff missing drift_purge entry: %+v", diff)
	}
	if dp["from"] != false || dp["to"] != true {
		t.Errorf("drift_purge diff = %+v, want {from:false, to:true}", dp)
	}

	// PATCH drift_purge=false.
	resp, body = s.do(t, "PATCH", "/api/v1/projects/pr/repos/deb/mirror-r1", cookie,
		map[string]any{"drift_purge": false})
	if resp.StatusCode != 200 {
		t.Fatalf("patch false status = %d; body=%+v", resp.StatusCode, body)
	}
	if v, ok := body["drift_purge"].(bool); !ok || v {
		t.Fatalf("PATCH false response drift_purge = %v, want false; body=%+v", body["drift_purge"], body)
	}
	resp, body = s.do(t, "GET", "/api/v1/projects/pr/repos/deb/mirror-r1", cookie, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("GET final status = %d; body=%+v", resp.StatusCode, body)
	}
	if v, ok := body["drift_purge"].(bool); !ok || v {
		t.Fatalf("GET final drift_purge = %v, want false; body=%+v", body["drift_purge"], body)
	}
}

// TestHandlePatchRepo_DriftPurgeFalseOnNonMirror_Allowed asserts that
// PATCH drift_purge=false on a non-mirror repo is a no-op (200), not a
// 400. Only setting drift_purge=true requires IsMirror=true; clearing
// the flag is always allowed (idempotent on default-false rows).
func TestHandlePatchRepo_DriftPurgeFalseOnNonMirror_Allowed(t *testing.T) {
	s := newTestServer(t)
	cookie := bootProjectAndRepo(t, s, "pr", "deb", "r1")
	resp, body := s.do(t, "PATCH", "/api/v1/projects/pr/repos/deb/r1", cookie,
		map[string]any{"drift_purge": false})
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200 (drift_purge=false on non-mirror is allowed); body=%+v",
			resp.StatusCode, body)
	}
	if v, ok := body["drift_purge"].(bool); !ok || v {
		t.Fatalf("response drift_purge = %v, want false; body=%+v", body["drift_purge"], body)
	}
}

func TestPatchRepo_AllowsFilterEdit(t *testing.T) {
	s := newTestServer(t)
	cookie := bootProjectOnly(t, s, "pr")
	if resp, body := s.do(t, "POST", "/api/v1/projects/pr/repos", cookie, map[string]any{
		"name":                "r1",
		"type":                "deb",
		"is_mirror":           true,
		"mirror_upstream_url": "https://archive.example/ubuntu",
		"mirror_filter":       map[string]any{"Suites": []string{"focal"}},
	}); resp.StatusCode != 200 {
		t.Fatalf("create mirror repo: %d %+v", resp.StatusCode, body)
	}
	newFilter := map[string]any{"Suites": []string{"focal", "bionic"}, "Components": []string{"main"}}
	resp, body := s.do(t, "PATCH", "/api/v1/projects/pr/repos/deb/r1", cookie, map[string]any{
		"mirror_filter": newFilter,
	})
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200; body=%+v", resp.StatusCode, body)
	}
	// Re-read the row.
	ctx := context.Background()
	var pid int64
	_ = s.db.Reader.QueryRowContext(ctx, `SELECT id FROM projects WHERE name='pr'`).Scan(&pid)
	rr, err := s.deps.Repos.FindByTriple(ctx, pid, "deb", "r1")
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if !strings.Contains(rr.MirrorFilterJSON, "bionic") {
		t.Errorf("filter not updated: %q", rr.MirrorFilterJSON)
	}
}
