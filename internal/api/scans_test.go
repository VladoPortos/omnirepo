package api_test

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/dxc-internal/omnirepo/internal/api"
	"github.com/dxc-internal/omnirepo/internal/audit"
	"github.com/dxc-internal/omnirepo/internal/auth"
	"github.com/dxc-internal/omnirepo/internal/httpx"
	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/metadata/sqlitetest"
	"github.com/dxc-internal/omnirepo/internal/storage"
	omrtls "github.com/dxc-internal/omnirepo/internal/tls"
)

// newScanRESTServer wires api.Mount with ScanDeps populated so the scan
// endpoints are mounted.
func newScanRESTServer(t *testing.T) *testServer {
	t.Helper()
	db := sqlitetest.New(t)
	dataRoot := t.TempDir()
	for _, d := range []string{"certs", "repos", "trash", "tmp", "logs", "sboms", "trivy/db"} {
		if err := os.MkdirAll(filepath.Join(dataRoot, d), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	// Pre-flight Trivy DB check in handleRescan requires trivy/db/trivy.db to
	// exist; create a placeholder so scan enqueue tests reach the real code
	// path instead of being 412'd by the missing-DB guard.
	if err := os.WriteFile(filepath.Join(dataRoot, "trivy", "db", "trivy.db"), []byte("placeholder"), 0o640); err != nil {
		t.Fatal(err)
	}
	auditLogger, err := audit.New(db, filepath.Join(dataRoot, "logs", "audit.log"), 10, 1)
	if err != nil {
		t.Fatal(err)
	}
	holder := omrtls.NewCertHolder()
	certPEM, keyPEM, err := omrtls.GenerateSelfSigned([]string{"localhost"}, time.Hour, 2048)
	if err != nil {
		t.Fatal(err)
	}
	if err := holder.Swap(certPEM, keyPEM); err != nil {
		t.Fatal(err)
	}

	deps := api.Deps{
		DB:       db,
		Users:    metadata.NewUsersRepo(db),
		Sessions: metadata.NewSessionsRepo(db),
		APIKeys:  metadata.NewAPIKeysRepo(db),
		Projects: metadata.NewProjectsRepo(db),
		Members:  metadata.NewMembersRepo(db),
		Repos:    metadata.NewReposRepo(db),
		Settings: metadata.NewSettingsRepo(db),
		Holder:   holder,
		DataRoot: dataRoot,
		Audit:    auditLogger,
		Trash:    storage.NewTrash(filepath.Join(dataRoot, "trash")),
		Locks:    storage.NewLocks(),
		ScanDeps: &api.ScansDeps{
			Scans:    metadata.NewScansRepo(db),
			Vulns:    metadata.NewVulnerabilitiesRepo(db),
			ScanKick: func() {},
			SBOMRoot: filepath.Join(dataRoot, "sboms"),
		},
	}

	mux := chi.NewRouter()
	mux.Get("/healthz", httpx.Healthz())
	api.Mount(mux, deps)

	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return &testServer{mux: mux, ts: ts, db: db, deps: deps, dataRoot: dataRoot}
}

// seedScanProject creates a project, a docker repo, and adds the user as a
// member. Returns (projectName, repoName, projectID, repoID).
func seedScanProject(t *testing.T, s *testServer, userID int64, repoType string) (string, string, int64, int64) {
	t.Helper()
	ctx := context.Background()
	pid, err := metadata.NewProjectsRepo(s.db).Create(ctx, "scanproj", "")
	if err != nil {
		t.Fatal(err)
	}
	rid, err := metadata.NewReposRepo(s.db).Create(ctx, pid, repoType, "img", "", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := metadata.NewMembersRepo(s.db).Add(ctx, pid, userID, "maintainer"); err != nil {
		t.Fatal(err)
	}
	return "scanproj", "img", pid, rid
}

func TestScansREST_Rescan_EnqueuesNewRow(t *testing.T) {
	s := newScanRESTServer(t)
	uid, pw := seedTestUser(t, s.db, "alice", "a@x", false, false)
	cookie, _, code := s.login(t, "alice", pw)
	if code != 200 {
		t.Fatalf("login code=%d", code)
	}
	proj, repo, _, repoID := seedScanProject(t, s, uid, "docker")

	resp, body := s.do(t, "POST",
		"/api/v1/projects/"+proj+"/repos/docker/"+repo+"/artifacts/sha256:abc/rescan",
		cookie, nil)
	if resp.StatusCode != 202 {
		t.Fatalf("rescan code=%d body=%v", resp.StatusCode, body)
	}
	sidF, ok := body["scan_id"].(float64)
	if !ok || sidF == 0 {
		t.Fatalf("scan_id missing: %v", body)
	}
	// Verify the row exists in DB.
	var status, kind, artifactID string
	var rid int64
	if err := s.db.Reader.QueryRowContext(context.Background(),
		`SELECT status, repo_id, artifact_kind, artifact_id FROM scans WHERE id=?`, int64(sidF),
	).Scan(&status, &rid, &kind, &artifactID); err != nil {
		t.Fatal(err)
	}
	if status != "pending" || rid != repoID || kind != "docker" || artifactID != "sha256:abc" {
		t.Fatalf("scan row wrong: status=%s rid=%d kind=%s id=%s", status, rid, kind, artifactID)
	}
}

// TestScansREST_RescanRepo_EnqueuesAllArtifacts — /repos/{...}/rescan
// must create one scans row for every artifact currently in the repo.
// Seed a docker repo with two distinct manifests, POST /rescan, and
// assert two new pending rows land in the scans table.
func TestScansREST_RescanRepo_EnqueuesAllArtifacts(t *testing.T) {
	s := newScanRESTServer(t)
	uid, pw := seedTestUser(t, s.db, "alice", "a@x", false, false)
	cookie, _, _ := s.login(t, "alice", pw)
	proj, repo, _, repoID := seedScanProject(t, s, uid, "docker")

	// Seed two manifests so the handler has something to iterate.
	ctx := context.Background()
	if err := s.db.WriteTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO docker_manifests(repo_id, digest, media_type, body, size_bytes) VALUES (?, ?, 'application/vnd.oci.image.manifest.v1+json', ?, ?)`,
			repoID, "sha256:aaa", []byte("{}"), 2,
		); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			`INSERT INTO docker_manifests(repo_id, digest, media_type, body, size_bytes) VALUES (?, ?, 'application/vnd.oci.image.manifest.v1+json', ?, ?)`,
			repoID, "sha256:bbb", []byte("{}"), 2,
		)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	resp, body := s.do(t, "POST",
		"/api/v1/projects/"+proj+"/repos/docker/"+repo+"/rescan",
		cookie, nil)
	if resp.StatusCode != 202 {
		t.Fatalf("rescan-all code=%d body=%v", resp.StatusCode, body)
	}
	if got, _ := body["enqueued"].(float64); got != 2 {
		t.Fatalf("enqueued=%v want 2 (body=%v)", got, body)
	}
	if body["repo_type"] != "docker" {
		t.Fatalf("repo_type=%v want docker", body["repo_type"])
	}
	// Confirm exactly two pending scan rows for this repo.
	var pending int
	if err := s.db.Reader.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM scans WHERE repo_id=? AND status='pending'`, repoID,
	).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 2 {
		t.Fatalf("pending rows=%d want 2", pending)
	}
}

// TestScansREST_RescanRepo_EmptyRepo — /rescan on a repo with no
// artifacts is not an error: returns 200 {enqueued:0} so the UI can
// surface "nothing to scan" without a red toast.
func TestScansREST_RescanRepo_EmptyRepo(t *testing.T) {
	s := newScanRESTServer(t)
	uid, pw := seedTestUser(t, s.db, "alice", "a@x", false, false)
	cookie, _, _ := s.login(t, "alice", pw)
	proj, repo, _, _ := seedScanProject(t, s, uid, "helm")

	resp, body := s.do(t, "POST",
		"/api/v1/projects/"+proj+"/repos/helm/"+repo+"/rescan",
		cookie, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("empty rescan code=%d body=%v", resp.StatusCode, body)
	}
	if got, _ := body["enqueued"].(float64); got != 0 {
		t.Fatalf("enqueued=%v want 0", got)
	}
}

// TestScansREST_PruneScans_KeepsLatest — prune must delete every
// finished (done/failed) scan row except the newest per artifact, and
// must never touch rows still in pending/running state.
func TestScansREST_PruneScans_KeepsLatest(t *testing.T) {
	s := newScanRESTServer(t)
	uid, pw := seedTestUser(t, s.db, "alice", "a@x", false, false)
	cookie, _, _ := s.login(t, "alice", pw)
	proj, repo, _, repoID := seedScanProject(t, s, uid, "docker")

	ctx := context.Background()
	scans := metadata.NewScansRepo(s.db)

	// Three finished scans for artifact A (should keep only the newest) +
	// two finished for artifact B (keep newest) + one running row (must
	// survive regardless).
	insertDoneScan := func(kind, artifactID string) int64 {
		t.Helper()
		var id int64
		if err := s.db.WriteTx(ctx, func(tx *sql.Tx) error {
			newID, err := scans.Enqueue(ctx, tx, repoID, kind, artifactID)
			if err != nil {
				return err
			}
			id = newID
			_, err = tx.ExecContext(ctx,
				`UPDATE scans SET status='done', finished_at=CURRENT_TIMESTAMP WHERE id=?`, id)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	insertDoneScan("docker", "sha256:a")
	insertDoneScan("docker", "sha256:a")
	keepA := insertDoneScan("docker", "sha256:a")
	insertDoneScan("docker", "sha256:b")
	keepB := insertDoneScan("docker", "sha256:b")
	// Running row for a third artifact — must survive the prune.
	var running int64
	if err := s.db.WriteTx(ctx, func(tx *sql.Tx) error {
		id, err := scans.Enqueue(ctx, tx, repoID, "docker", "sha256:c")
		running = id
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE scans SET status='running' WHERE id=?`, id)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	resp, body := s.do(t, "POST",
		"/api/v1/projects/"+proj+"/repos/docker/"+repo+"/scans/prune",
		cookie, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("prune code=%d body=%v", resp.StatusCode, body)
	}
	if got, _ := body["deleted"].(float64); got != 3 {
		t.Fatalf("deleted=%v want 3 (body=%v)", got, body)
	}

	// What remains: keepA, keepB, running — three rows total.
	var ids []int64
	rows, err := s.db.Reader.QueryContext(ctx,
		`SELECT id FROM scans WHERE repo_id=? ORDER BY id`, repoID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	want := map[int64]bool{keepA: true, keepB: true, running: true}
	if len(ids) != len(want) {
		t.Fatalf("surviving ids=%v want %v", ids, want)
	}
	for _, id := range ids {
		if !want[id] {
			t.Fatalf("unexpected surviving id=%d (want %v)", id, want)
		}
	}
}

// TestScansREST_RescanRepo_GitRejected — git repos have no scannable
// artifacts; the endpoint must surface a clear 400 instead of silently
// enqueuing nothing.
func TestScansREST_RescanRepo_GitRejected(t *testing.T) {
	s := newScanRESTServer(t)
	uid, pw := seedTestUser(t, s.db, "alice", "a@x", false, false)
	cookie, _, _ := s.login(t, "alice", pw)
	proj, repo, _, _ := seedScanProject(t, s, uid, "git")

	resp, _ := s.do(t, "POST",
		"/api/v1/projects/"+proj+"/repos/git/"+repo+"/rescan",
		cookie, nil)
	if resp.StatusCode != 400 {
		t.Fatalf("git rescan code=%d want 400", resp.StatusCode)
	}
}

func TestScansREST_CrossProjectAccessDenied(t *testing.T) {
	s := newScanRESTServer(t)
	aliceID, alicePW := seedTestUser(t, s.db, "alice", "a@x", false, false)
	_, bobPW := seedTestUser(t, s.db, "bob", "b@x", false, false)
	aliceCookie, _, _ := s.login(t, "alice", alicePW)
	bobCookie, _, _ := s.login(t, "bob", bobPW)

	proj, repo, _, _ := seedScanProject(t, s, aliceID, "docker")

	// Bob is NOT in scanproj; rescan must 403.
	resp, _ := s.do(t, "POST",
		"/api/v1/projects/"+proj+"/repos/docker/"+repo+"/artifacts/sha256:x/rescan",
		bobCookie, nil)
	if resp.StatusCode != 403 {
		t.Fatalf("bob expected 403, got %d", resp.StatusCode)
	}

	// Alice can.
	resp2, _ := s.do(t, "POST",
		"/api/v1/projects/"+proj+"/repos/docker/"+repo+"/artifacts/sha256:x/rescan",
		aliceCookie, nil)
	if resp2.StatusCode != 202 {
		t.Fatalf("alice expected 202, got %d", resp2.StatusCode)
	}
}

func TestScansREST_ListArtifactScans(t *testing.T) {
	s := newScanRESTServer(t)
	uid, pw := seedTestUser(t, s.db, "alice", "a@x", false, false)
	cookie, _, _ := s.login(t, "alice", pw)
	proj, repo, _, repoID := seedScanProject(t, s, uid, "docker")

	// Seed two scans.
	ctx := context.Background()
	scans := metadata.NewScansRepo(s.db)
	if err := s.db.WriteTx(ctx, func(tx *sql.Tx) error {
		if _, err := scans.Enqueue(ctx, tx, repoID, "docker", "sha256:abc"); err != nil {
			return err
		}
		_, err := scans.Enqueue(ctx, tx, repoID, "docker", "sha256:abc")
		return err
	}); err != nil {
		t.Fatal(err)
	}

	req, _ := http.NewRequest("GET",
		s.ts.URL+"/api/v1/projects/"+proj+"/repos/docker/"+repo+"/artifacts/sha256:abc/scans",
		nil)
	req.AddCookie(&http.Cookie{Name: "omnirepo_session", Value: cookie})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		t.Fatalf("list code=%d", resp.StatusCode)
	}
}

func TestScansREST_GetSBOMStreamsFile(t *testing.T) {
	s := newScanRESTServer(t)
	uid, pw := seedTestUser(t, s.db, "alice", "a@x", false, false)
	cookie, _, _ := s.login(t, "alice", pw)
	_, _, _, repoID := seedScanProject(t, s, uid, "docker")

	// Write a sample SBOM file under sboms/.
	sbomDir := filepath.Join(s.dataRoot, "sboms")
	if err := os.MkdirAll(sbomDir, 0o750); err != nil {
		t.Fatal(err)
	}

	// Insert a scan row pointing at an existing SBOM file.
	ctx := context.Background()
	scans := metadata.NewScansRepo(s.db)
	var sid int64
	if err := s.db.WriteTx(ctx, func(tx *sql.Tx) error {
		s2, err := scans.Enqueue(ctx, tx, repoID, "docker", "sha256:withsbom")
		if err != nil {
			return err
		}
		sid = s2
		// Mark done with sbom_path set.
		sbomPath := filepath.Join(sbomDir, "test.json")
		if err := os.WriteFile(sbomPath, []byte(`{"bomFormat":"CycloneDX"}`), 0o640); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE scans SET status='done', finished_at=CURRENT_TIMESTAMP, sbom_path=? WHERE id=?`,
			sbomPath, sid); err != nil {
			return err
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// GET /api/v1/scans/{id}/sbom
	req, _ := http.NewRequest("GET",
		s.ts.URL+"/api/v1/scans/"+strconvI(sid)+"/sbom", nil)
	req.AddCookie(&http.Cookie{Name: "omnirepo_session", Value: cookie})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		t.Fatalf("sbom code=%d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type=%q want application/json", ct)
	}
}

// TestScansREST_Rescan_UserOwnedAPIKey_Accepted is the F-05.1 regression for
// the tenth site Codex caught after the initial fix: Deps.actorIsProjectMember
// used to early-return on `actor.Kind != ActorKindUser`, denying user-owned
// API keys on every scan/content/artifact endpoint even when the owning user
// was a project member. The fix drops that Kind guard so Actor.ID (already
// the owning user's id per the Actor.ID doc) flows into the project_members
// lookup.
//
// Wire: seed alice + a user-owned API key, add her to the project, POST
// rescan with HTTP Basic (alice / raw token). A session-cookie path is
// already covered by TestScansREST_Rescan_EnqueuesNewRow — this one pins the
// API-key path that was broken.
func TestScansREST_Rescan_UserOwnedAPIKey_Accepted(t *testing.T) {
	s := newScanRESTServer(t)
	uid, _ := seedTestUser(t, s.db, "alice", "a@x", false, false)
	proj, repo, _, _ := seedScanProject(t, s, uid, "docker")

	k, err := auth.GenerateAPIKey(auth.APIKeyKindUser)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	if _, err := metadata.NewAPIKeysRepo(s.db).CreateUserKey(
		context.Background(), uid, "alice-dev", k.Prefix, k.SHA256,
	); err != nil {
		t.Fatalf("create key: %v", err)
	}

	req, _ := http.NewRequest("POST",
		s.ts.URL+"/api/v1/projects/"+proj+"/repos/docker/"+repo+"/artifacts/sha256:abc/rescan",
		nil)
	req.SetBasicAuth("alice", k.Plaintext)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 202 {
		t.Fatalf("user-owned API key rescan got status=%d, want 202 (F-05.1 regression)", resp.StatusCode)
	}
}

func strconvI(i int64) string {
	const digits = "0123456789"
	if i == 0 {
		return "0"
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = digits[i%10]
		i /= 10
	}
	return string(b[pos:])
}
