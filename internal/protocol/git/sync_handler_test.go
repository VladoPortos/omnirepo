package git_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v6"
	gogitconfig "github.com/go-git/go-git/v6/config"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"

	"github.com/vladoportos/omnirepo/internal/audit"
	"github.com/vladoportos/omnirepo/internal/config"
	omrcrypto "github.com/vladoportos/omnirepo/internal/crypto"
	"github.com/vladoportos/omnirepo/internal/metadata"
	"github.com/vladoportos/omnirepo/internal/metadata/sqlitetest"
	gitpkg "github.com/vladoportos/omnirepo/internal/protocol/git"
)

// --- Test helpers ---

// makeUpstreamBareRepo builds a bare repo on disk seeded with one commit
// (carrying README.md + optional .gitattributes for LFS-detection tests),
// a main branch, and a v1.0 lightweight tag. No `git` CLI dependency —
// uses go-git in-process for full hermetic-ness.
func makeUpstreamBareRepo(t *testing.T, extraGitattributes string) string {
	t.Helper()
	root := t.TempDir()
	bareDir := filepath.Join(root, "upstream.git")
	workDir := filepath.Join(root, "work")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("mkdir work: %v", err)
	}

	// Build a non-bare working repo, commit, then mirror-fetch into the bare.
	wRepo, err := gogit.PlainInit(workDir, false)
	if err != nil {
		t.Fatalf("PlainInit work: %v", err)
	}
	wt, err := wRepo.Worktree()
	if err != nil {
		t.Fatalf("Worktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "README.md"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	if _, err := wt.Add("README.md"); err != nil {
		t.Fatalf("add README: %v", err)
	}
	if extraGitattributes != "" {
		if err := os.WriteFile(filepath.Join(workDir, ".gitattributes"), []byte(extraGitattributes), 0o644); err != nil {
			t.Fatalf("write .gitattributes: %v", err)
		}
		if _, err := wt.Add(".gitattributes"); err != nil {
			t.Fatalf("add .gitattributes: %v", err)
		}
	}
	if _, err := wt.Commit("init", &gogit.CommitOptions{
		Author: &object.Signature{Name: "T", Email: "t@t.com", When: time.Now()},
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}
	headRef, err := wRepo.Head()
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	if _, err := wRepo.CreateTag("v1.0", headRef.Hash(), nil); err != nil {
		t.Fatalf("CreateTag: %v", err)
	}

	// Init the bare upstream and mirror-fetch from the working repo via
	// the in-process file:// transport.
	bareRepo, err := gogit.PlainInit(bareDir, true)
	if err != nil {
		t.Fatalf("PlainInit bare: %v", err)
	}
	if _, err := bareRepo.CreateRemote(&gogitconfig.RemoteConfig{
		Name:   "origin",
		URLs:   []string{"file://" + workDir},
		Fetch:  []gogitconfig.RefSpec{"+refs/*:refs/*"},
		Mirror: true,
	}); err != nil {
		t.Fatalf("CreateRemote: %v", err)
	}
	if err := bareRepo.FetchContext(context.Background(), &gogit.FetchOptions{
		RemoteName: "origin",
		Tags:       plumbing.AllTags,
		Force:      true,
	}); err != nil && err != gogit.NoErrAlreadyUpToDate {
		t.Fatalf("fetch into bare: %v", err)
	}

	// Ensure HEAD points at refs/heads/<branch>.
	branchName := "main"
	if headRef.Type() == plumbing.SymbolicReference {
		t := string(headRef.Target())
		if len(t) > len("refs/heads/") {
			branchName = t[len("refs/heads/"):]
		}
	}
	headRefName := plumbing.ReferenceName("refs/heads/" + branchName)
	if err := bareRepo.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, headRefName)); err != nil {
		t.Fatalf("SetReference HEAD: %v", err)
	}
	if err := bareRepo.Storer.SetReference(plumbing.NewHashReference(headRefName, headRef.Hash())); err != nil {
		t.Fatalf("SetReference branch: %v", err)
	}

	return bareDir
}

// recordingAuditLogger captures audit events for assertion. Implements audit.Logger.
type recordingAuditLogger struct{ events []audit.Event }

func (r *recordingAuditLogger) Record(_ context.Context, e audit.Event) error {
	r.events = append(r.events, e)
	return nil
}

func (r *recordingAuditLogger) firstOf(kind audit.EventKind) *audit.Event {
	for i := range r.events {
		if r.events[i].Kind == kind {
			return &r.events[i]
		}
	}
	return nil
}

// auditKinds returns just the EventKind slice for diagnostic output.
func auditKinds(events []audit.Event) []string {
	out := make([]string, len(events))
	for i, e := range events {
		out[i] = string(e.Kind)
	}
	return out
}

// seedMirrorRepo inserts a project + git mirror repos row.
func seedMirrorRepo(t *testing.T, db *metadata.DB, projName, repoName, upstreamURL string) (projectID, repoID int64) {
	t.Helper()
	ctx := context.Background()
	res, err := db.Writer.ExecContext(ctx, `INSERT INTO projects(name) VALUES (?)`, projName)
	if err != nil {
		t.Fatalf("insert project: %v", err)
	}
	projectID, _ = res.LastInsertId()
	err = db.WriteTx(ctx, func(tx *sql.Tx) error {
		var insErr error
		repoID, insErr = metadata.NewReposRepo(db).CreateInTx(ctx, tx, projectID, "git", repoName, "", nil, nil, nil)
		if insErr != nil {
			return insErr
		}
		return metadata.NewReposRepo(db).SetMirrorConfigInTx(ctx, tx, repoID, metadata.MirrorConfig{
			IsMirror:    true,
			UpstreamURL: upstreamURL,
		})
	})
	if err != nil {
		t.Fatalf("seed mirror: %v", err)
	}
	return projectID, repoID
}

// newTestAEAD constructs a throwaway AEAD for the UpstreamCredsRepo
// constructor. None of the tests in this file exercise cred decryption
// (CredID is nil throughout), so the AEAD's keys are never used — but the
// constructor signature requires a non-nil *crypto.AEAD.
func newTestAEAD(t *testing.T) *omrcrypto.AEAD {
	t.Helper()
	key, err := omrcrypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	a, err := omrcrypto.New(key)
	if err != nil {
		t.Fatalf("crypto.New: %v", err)
	}
	return a
}

func newSyncHandlerForTest(t *testing.T, db *metadata.DB) (*gitpkg.SyncHandler, *recordingAuditLogger, string) {
	t.Helper()
	dataRoot := t.TempDir()
	rec := &recordingAuditLogger{}
	h := gitpkg.NewSyncHandler(gitpkg.SyncDeps{
		DB:         db,
		Repos:      metadata.NewReposRepo(db),
		Projects:   metadata.NewProjectsRepo(db),
		Refs:       metadata.NewGitRefsRepo(db),
		Creds:      metadata.NewUpstreamCredsRepo(db, newTestAEAD(t)),
		Audit:      rec,
		HTTPClient: &http.Client{},
		DataRoot:   dataRoot,
		Cfg:        config.SyncConfig{UpstreamHTTPTimeout: 30 * time.Second},
		SyncJobs:   metadata.NewSyncJobsRepo(db),
	})
	return h, rec, dataRoot
}

// fileURL returns a properly-encoded file:// URL for an OS path.
func fileURL(p string) string {
	u := url.URL{Scheme: "file", Path: p}
	return u.String()
}

// --- Tests ---

// TestGitSync_FirstSync drives the handler at an empty bare-repo path
// against an in-process bare upstream. PlainCloneContext branch is taken;
// after the run, .git/config exists, refs are populated, and audit emits
// EvtSyncStarted + EvtSyncFinished with the load-bearing detail keys.
func TestGitSync_FirstSync(t *testing.T) {
	upstream := makeUpstreamBareRepo(t, "")
	db := sqlitetest.New(t)
	projectID, repoID := seedMirrorRepo(t, db, "p1", "r1", fileURL(upstream))
	h, rec, dataRoot := newSyncHandlerForTest(t, db)

	payload, _ := json.Marshal(gitpkg.SyncPayload{UpstreamURL: fileURL(upstream)})
	if err := h.Handle(context.Background(), string(payload), projectID, repoID, 1); err != nil {
		t.Fatalf("Handle first-sync: %v", err)
	}

	bareDir := filepath.Join(dataRoot, "repos", "p1", "git", "r1.git")
	if _, err := os.Stat(filepath.Join(bareDir, "config")); err != nil {
		t.Fatalf("bare repo config missing after first sync: %v", err)
	}

	got, err := metadata.NewGitRefsRepo(db).List(context.Background(), repoID)
	if err != nil {
		t.Fatalf("list refs: %v", err)
	}
	if len(got) == 0 {
		t.Fatalf("git_refs empty after first sync")
	}
	want := map[string]bool{"HEAD": false, "refs/tags/v1.0": false}
	for _, g := range got {
		if _, ok := want[g.Name]; ok {
			want[g.Name] = true
		}
	}
	for k, found := range want {
		if !found {
			t.Errorf("expected ref %q not found in git_refs (got %+v)", k, got)
		}
	}

	if rec.firstOf(audit.EvtSyncStarted) == nil {
		t.Fatal("missing EvtSyncStarted")
	}
	finished := rec.firstOf(audit.EvtSyncFinished)
	if finished == nil {
		t.Fatalf("missing EvtSyncFinished; got %v", auditKinds(rec.events))
	}
	for _, k := range []string{"refs_updated", "objects_received", "duration_ms"} {
		if _, ok := finished.Details[k]; !ok {
			t.Errorf("EvtSyncFinished missing detail %q; got %+v", k, finished.Details)
		}
	}
	if rec.firstOf(audit.EvtSyncFailed) != nil {
		t.Errorf("unexpected EvtSyncFailed: %+v", rec.firstOf(audit.EvtSyncFailed))
	}
}

// TestGitSync_ReSync proves the FetchContext branch is used on second
// invocation: .git/config inode/identity stays stable across the re-sync
// (clone would have recreated the file).
func TestGitSync_ReSync(t *testing.T) {
	upstream := makeUpstreamBareRepo(t, "")
	db := sqlitetest.New(t)
	projectID, repoID := seedMirrorRepo(t, db, "p2", "r2", fileURL(upstream))
	h, _, dataRoot := newSyncHandlerForTest(t, db)
	payload, _ := json.Marshal(gitpkg.SyncPayload{UpstreamURL: fileURL(upstream)})

	if err := h.Handle(context.Background(), string(payload), projectID, repoID, 1); err != nil {
		t.Fatalf("first sync: %v", err)
	}
	bareDir := filepath.Join(dataRoot, "repos", "p2", "git", "r2.git")
	configPath := filepath.Join(bareDir, "config")
	preStat, err := os.Stat(configPath)
	if err != nil {
		t.Fatalf("stat pre-resync: %v", err)
	}

	if err := h.Handle(context.Background(), string(payload), projectID, repoID, 2); err != nil {
		t.Fatalf("re-sync: %v", err)
	}
	postStat, err := os.Stat(configPath)
	if err != nil {
		t.Fatalf("stat post-resync: %v", err)
	}
	if !os.SameFile(preStat, postStat) {
		t.Fatalf(".git/config recreated on re-sync — expected fetch path, got clone")
	}

	got, _ := metadata.NewGitRefsRepo(db).List(context.Background(), repoID)
	if len(got) == 0 {
		t.Fatal("refs empty after re-sync")
	}
}

// TestGitSync_LFSDetection: an upstream repo carrying .gitattributes with
// filter=lfs triggers EvtMirrorSyncLFSDetected with sample_paths populated.
func TestGitSync_LFSDetection(t *testing.T) {
	const lfsAttrs = "*.bin filter=lfs diff=lfs merge=lfs -text\n"
	upstream := makeUpstreamBareRepo(t, lfsAttrs)
	db := sqlitetest.New(t)
	projectID, repoID := seedMirrorRepo(t, db, "p3", "r3", fileURL(upstream))
	h, rec, _ := newSyncHandlerForTest(t, db)
	payload, _ := json.Marshal(gitpkg.SyncPayload{UpstreamURL: fileURL(upstream)})

	if err := h.Handle(context.Background(), string(payload), projectID, repoID, 1); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	lfs := rec.firstOf(audit.EvtMirrorSyncLFSDetected)
	if lfs == nil {
		t.Fatalf("missing EvtMirrorSyncLFSDetected; events: %v", auditKinds(rec.events))
	}
	samples, ok := lfs.Details["sample_paths"].([]string)
	if !ok {
		t.Fatalf("sample_paths is not []string: %T", lfs.Details["sample_paths"])
	}
	found := false
	for _, p := range samples {
		if filepath.Base(p) == ".gitattributes" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected .gitattributes in sample_paths, got %v", samples)
	}
}

// TestGitSync_ErrorPath: a nonexistent file:// URL must fail loudly,
// emit EvtSyncFailed, and leave git_refs untouched.
func TestGitSync_ErrorPath(t *testing.T) {
	db := sqlitetest.New(t)
	projectID, repoID := seedMirrorRepo(t, db, "p4", "r4", "file:///definitely/not/here.git")

	priorRef := metadata.GitRef{Name: "refs/heads/preexisting", Target: "deadbeef", Type: metadata.GitRefBranch}
	if err := db.WriteTx(context.Background(), func(tx *sql.Tx) error {
		return metadata.NewGitRefsRepo(db).ReplaceAllTx(context.Background(), tx, repoID, []metadata.GitRef{priorRef})
	}); err != nil {
		t.Fatalf("seed prior ref: %v", err)
	}

	h, rec, _ := newSyncHandlerForTest(t, db)
	payload, _ := json.Marshal(gitpkg.SyncPayload{UpstreamURL: "file:///definitely/not/here.git"})

	if err := h.Handle(context.Background(), string(payload), projectID, repoID, 1); err == nil {
		t.Fatal("expected error for nonexistent upstream")
	}
	if rec.firstOf(audit.EvtSyncFailed) == nil {
		t.Fatalf("missing EvtSyncFailed; events: %v", auditKinds(rec.events))
	}
	if rec.firstOf(audit.EvtSyncFinished) != nil {
		t.Fatalf("EvtSyncFinished should NOT be emitted on error path")
	}

	got, _ := metadata.NewGitRefsRepo(db).List(context.Background(), repoID)
	if len(got) != 1 || got[0].Name != "refs/heads/preexisting" {
		t.Errorf("git_refs mutated on error path: got %+v", got)
	}
}

// TestGitSync_OnRepoCreate_SkipsInitBare_Smoke is the integration of
// Task 1 + Task 2b: CreateRepoHook skips InitBare for the mirror repo, then
// the sync handler's PlainCloneContext branch happily lands the clone
// at the empty target path. Pitfall D guard end-to-end.
func TestGitSync_OnRepoCreate_SkipsInitBare_Smoke(t *testing.T) {
	upstream := makeUpstreamBareRepo(t, "")
	db := sqlitetest.New(t)
	dataRoot := t.TempDir()

	ctx := context.Background()
	res, err := db.Writer.ExecContext(ctx, `INSERT INTO projects(name) VALUES ('smokep')`)
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	projectID, _ := res.LastInsertId()
	var repoID int64
	err = db.WriteTx(ctx, func(tx *sql.Tx) error {
		var insErr error
		repoID, insErr = metadata.NewReposRepo(db).CreateInTx(ctx, tx, projectID, "git", "smoker", "", nil, nil, nil)
		if insErr != nil {
			return insErr
		}
		if err := metadata.NewReposRepo(db).SetMirrorConfigInTx(ctx, tx, repoID, metadata.MirrorConfig{
			IsMirror:    true,
			UpstreamURL: fileURL(upstream),
		}); err != nil {
			return err
		}
		return gitpkg.CreateRepoHook(ctx, tx, repoID, "git", "smokep", "smoker", dataRoot, metadata.NewGitRefsRepo(db))
	})
	if err != nil {
		t.Fatalf("create+hook: %v", err)
	}

	bareDir := filepath.Join(dataRoot, "repos", "smokep", "git", "smoker.git")
	if _, err := os.Stat(bareDir); !os.IsNotExist(err) {
		t.Fatalf("bare repo should NOT exist post-CreateRepoHook for mirror: stat err=%v", err)
	}

	rec := &recordingAuditLogger{}
	syncH := gitpkg.NewSyncHandler(gitpkg.SyncDeps{
		DB:         db,
		Repos:      metadata.NewReposRepo(db),
		Projects:   metadata.NewProjectsRepo(db),
		Refs:       metadata.NewGitRefsRepo(db),
		Creds:      metadata.NewUpstreamCredsRepo(db, newTestAEAD(t)),
		Audit:      rec,
		HTTPClient: &http.Client{},
		DataRoot:   dataRoot,
		Cfg:        config.SyncConfig{UpstreamHTTPTimeout: 30 * time.Second},
		SyncJobs:   metadata.NewSyncJobsRepo(db),
	})
	payload, _ := json.Marshal(gitpkg.SyncPayload{UpstreamURL: fileURL(upstream)})
	if err := syncH.Handle(ctx, string(payload), projectID, repoID, 1); err != nil {
		t.Fatalf("Handle (smoke): %v", err)
	}
	if _, err := os.Stat(filepath.Join(bareDir, "config")); err != nil {
		t.Fatalf("bare config missing after smoke first-sync: %v", err)
	}
	if rec.firstOf(audit.EvtSyncFailed) != nil {
		t.Fatalf("unexpected EvtSyncFailed in smoke: %+v", rec.firstOf(audit.EvtSyncFailed))
	}
}
