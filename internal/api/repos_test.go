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
