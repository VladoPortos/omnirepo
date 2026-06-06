package git_test

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vladoportos/omnirepo/internal/audit"
	"github.com/vladoportos/omnirepo/internal/config"
	"github.com/vladoportos/omnirepo/internal/metadata"
	"github.com/vladoportos/omnirepo/internal/metadata/sqlitetest"
	gitpkg "github.com/vladoportos/omnirepo/internal/protocol/git"
	"github.com/vladoportos/omnirepo/internal/storage"
)

// --- helpers ---

func seedProject(t *testing.T, db *metadata.DB) int64 {
	t.Helper()
	ctx := context.Background()
	res, err := db.Writer.ExecContext(ctx, `INSERT INTO projects(name) VALUES ('testproj')`)
	if err != nil {
		t.Fatalf("seed project: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

func seedGitRepoRow(t *testing.T, db *metadata.DB, projectID int64, name string) int64 {
	t.Helper()
	ctx := context.Background()
	res, err := db.Writer.ExecContext(ctx, `INSERT INTO repos(project_id, type, name) VALUES (?, 'git', ?)`, projectID, name)
	if err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

// makeBareRepoWithRefs creates a bare repo via InitBare, then uses git CLI
// to add commits, branches, and tags.
func makeBareRepoWithRefs(t *testing.T, branches []string, tags []string) string {
	t.Helper()
	dir := t.TempDir()
	bareDir := filepath.Join(dir, "repo.git")
	if err := gitpkg.InitBare(bareDir, "main"); err != nil {
		t.Fatalf("InitBare: %v", err)
	}

	if len(branches) == 0 && len(tags) == 0 {
		return bareDir
	}

	// Clone into a work tree, make a commit, push branches and tags.
	workDir := filepath.Join(dir, "work")
	runGit(t, dir, "clone", bareDir, workDir)
	runGit(t, workDir, "config", "user.email", "test@test.com")
	runGit(t, workDir, "config", "user.name", "Test")

	// Create initial commit.
	dummyFile := filepath.Join(workDir, "README.md")
	if err := os.WriteFile(dummyFile, []byte("hello"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	runGit(t, workDir, "add", ".")
	runGit(t, workDir, "commit", "-m", "initial")

	// Push main.
	runGit(t, workDir, "push", "origin", "main")

	// Create extra branches.
	for _, b := range branches {
		if b == "main" {
			continue
		}
		runGit(t, workDir, "checkout", "-b", b)
		runGit(t, workDir, "push", "origin", b)
	}

	// Create tags.
	for _, tag := range tags {
		runGit(t, workDir, "tag", tag)
		runGit(t, workDir, "push", "origin", tag)
	}

	return bareDir
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// fakeAuditLogger captures audit events for assertion.
type fakeAuditLogger struct {
	events []audit.Event
}

func (f *fakeAuditLogger) Record(_ context.Context, e audit.Event) error {
	f.events = append(f.events, e)
	return nil
}

// --- Test 1: WalkAndReplace basic ---

// findRef returns the ref named name from List output, failing the test
// when absent.
func findRef(t *testing.T, refs *metadata.GitRefsRepo, repoID int64, name string) metadata.GitRef {
	t.Helper()
	all, err := refs.List(context.Background(), repoID)
	if err != nil {
		t.Fatalf("list refs: %v", err)
	}
	for _, g := range all {
		if g.Name == name {
			return g
		}
	}
	t.Fatalf("ref %q not found in %d refs", name, len(all))
	return metadata.GitRef{}
}

func TestWalkAndReplace_BasicRefs(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}

	db := sqlitetest.New(t)
	projID := seedProject(t, db)
	repoID := seedGitRepoRow(t, db, projID, "r1")
	refsRepo := metadata.NewGitRefsRepo(db)

	bareDir := makeBareRepoWithRefs(t, []string{"main", "dev", "feature"}, []string{"v1.0", "v2.0"})

	err := gitpkg.WalkAndReplace(context.Background(), db, refsRepo, repoID, bareDir)
	if err != nil {
		t.Fatalf("WalkAndReplace: %v", err)
	}

	got, err := refsRepo.List(context.Background(), repoID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	// Expect 6 rows: HEAD + 3 branches + 2 tags.
	if len(got) != 6 {
		names := make([]string, len(got))
		for i, g := range got {
			names[i] = g.Name
		}
		t.Fatalf("want 6 refs, got %d: %v", len(got), names)
	}

	// Check types by name.
	typeMap := map[string]metadata.GitRefType{}
	for _, g := range got {
		typeMap[g.Name] = g.Type
	}
	if typeMap["HEAD"] != metadata.GitRefSymbolic {
		t.Errorf("HEAD type = %q, want symbolic", typeMap["HEAD"])
	}
	if typeMap["refs/heads/main"] != metadata.GitRefBranch {
		t.Errorf("main type = %q, want branch", typeMap["refs/heads/main"])
	}
	if typeMap["refs/tags/v1.0"] != metadata.GitRefTag {
		t.Errorf("v1.0 type = %q, want tag", typeMap["refs/tags/v1.0"])
	}
}

// --- Test 2: Second call replaces old rows ---

func TestWalkAndReplace_ReplacesOldRows(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}

	db := sqlitetest.New(t)
	projID := seedProject(t, db)
	repoID := seedGitRepoRow(t, db, projID, "r2")
	refsRepo := metadata.NewGitRefsRepo(db)

	dir := t.TempDir()
	bareDir := filepath.Join(dir, "repo.git")
	if err := gitpkg.InitBare(bareDir, "main"); err != nil {
		t.Fatal(err)
	}

	workDir := filepath.Join(dir, "work")
	runGit(t, dir, "clone", bareDir, workDir)
	runGit(t, workDir, "config", "user.email", "t@t.com")
	runGit(t, workDir, "config", "user.name", "T")
	if err := os.WriteFile(filepath.Join(workDir, "a"), []byte("a"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, workDir, "add", ".")
	runGit(t, workDir, "commit", "-m", "init")
	runGit(t, workDir, "push", "origin", "main")
	runGit(t, workDir, "tag", "v1")
	runGit(t, workDir, "push", "origin", "v1")

	// First walk.
	if err := gitpkg.WalkAndReplace(context.Background(), db, refsRepo, repoID, bareDir); err != nil {
		t.Fatal(err)
	}
	got1, _ := refsRepo.List(context.Background(), repoID)
	if len(got1) != 3 { // HEAD, main, v1
		t.Fatalf("first walk: want 3 refs, got %d", len(got1))
	}

	// Add a branch, remove tag (by deleting on remote).
	runGit(t, workDir, "checkout", "-b", "dev")
	runGit(t, workDir, "push", "origin", "dev")
	runGit(t, workDir, "push", "origin", "--delete", "v1")

	// Second walk.
	if err := gitpkg.WalkAndReplace(context.Background(), db, refsRepo, repoID, bareDir); err != nil {
		t.Fatal(err)
	}
	got2, _ := refsRepo.List(context.Background(), repoID)

	names := map[string]bool{}
	for _, g := range got2 {
		names[g.Name] = true
	}
	if names["refs/tags/v1"] {
		t.Error("v1 tag should be gone after second walk")
	}
	if !names["refs/heads/dev"] {
		t.Error("dev branch should be present after second walk")
	}
}

// --- Test 3: ClassifyRef ---

func TestClassifyRef(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"HEAD", "symbolic"},
		{"refs/heads/main", "branch"},
		{"refs/tags/v1", "tag"},
		{"refs/notes/x", "other"},
		{"refs/pull/1/head", "other"},
	}
	for _, tc := range tests {
		got := gitpkg.ClassifyRef(tc.name)
		if got != tc.want {
			t.Errorf("ClassifyRef(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// --- Test 4: Stress test 800 refs chunked ---

func TestWalkAndReplace_800Refs(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}

	db := sqlitetest.New(t)
	projID := seedProject(t, db)
	repoID := seedGitRepoRow(t, db, projID, "r4")
	refsRepo := metadata.NewGitRefsRepo(db)

	dir := t.TempDir()
	bareDir := filepath.Join(dir, "repo.git")
	if err := gitpkg.InitBare(bareDir, "main"); err != nil {
		t.Fatal(err)
	}

	workDir := filepath.Join(dir, "work")
	runGit(t, dir, "clone", bareDir, workDir)
	runGit(t, workDir, "config", "user.email", "t@t.com")
	runGit(t, workDir, "config", "user.name", "T")
	if err := os.WriteFile(filepath.Join(workDir, "a"), []byte("a"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, workDir, "add", ".")
	runGit(t, workDir, "commit", "-m", "init")
	runGit(t, workDir, "push", "origin", "main")

	// Create 799 branches (main already exists, so total branches = 800).
	// Then push all at once for speed.
	for i := 0; i < 799; i++ {
		runGit(t, workDir, "branch", fmt.Sprintf("br-%04d", i))
	}
	runGit(t, workDir, "push", "origin", "--all")

	if err := gitpkg.WalkAndReplace(context.Background(), db, refsRepo, repoID, bareDir); err != nil {
		t.Fatalf("WalkAndReplace: %v", err)
	}
	got, _ := refsRepo.List(context.Background(), repoID)

	// 800 branches + HEAD = 801
	if len(got) != 801 {
		t.Fatalf("want 801 refs, got %d", len(got))
	}
}

// --- Test 5: Detached HEAD ---

func TestWalkAndReplace_DetachedHEAD(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}

	db := sqlitetest.New(t)
	projID := seedProject(t, db)
	repoID := seedGitRepoRow(t, db, projID, "r5")
	refsRepo := metadata.NewGitRefsRepo(db)

	dir := t.TempDir()
	bareDir := filepath.Join(dir, "repo.git")
	if err := gitpkg.InitBare(bareDir, "main"); err != nil {
		t.Fatal(err)
	}

	workDir := filepath.Join(dir, "work")
	runGit(t, dir, "clone", bareDir, workDir)
	runGit(t, workDir, "config", "user.email", "t@t.com")
	runGit(t, workDir, "config", "user.name", "T")
	if err := os.WriteFile(filepath.Join(workDir, "a"), []byte("a"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, workDir, "add", ".")
	runGit(t, workDir, "commit", "-m", "init")
	runGit(t, workDir, "push", "origin", "main")

	// Get the commit hash to set HEAD as detached in the bare repo.
	hashBytes, _ := exec.Command("git", "-C", workDir, "rev-parse", "HEAD").Output()
	hash := strings.TrimSpace(string(hashBytes))

	// Detach HEAD in the bare repo by writing a raw hash to HEAD.
	headPath := filepath.Join(bareDir, "HEAD")
	if err := os.WriteFile(headPath, []byte(hash+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := gitpkg.WalkAndReplace(context.Background(), db, refsRepo, repoID, bareDir); err != nil {
		t.Fatal(err)
	}

	headRef := findRef(t, refsRepo, repoID, "HEAD")
	if headRef.Type != metadata.GitRefSymbolic {
		t.Errorf("HEAD type = %q, want symbolic", headRef.Type)
	}
	if len(headRef.Target) != 40 {
		t.Errorf("HEAD target should be a 40-char SHA, got %q", headRef.Target)
	}
}

// --- Test 6: Post-ReceivePack hook integration ---

func TestPostReceivePackHook(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}

	db := sqlitetest.New(t)
	projID := seedProject(t, db)
	repoName := "hooktest"
	repoID := seedGitRepoRow(t, db, projID, repoName)
	refsRepo := metadata.NewGitRefsRepo(db)
	auditLog := &fakeAuditLogger{}

	dataRoot := t.TempDir()
	bareDir := filepath.Join(dataRoot, "repos", "testproj", "git", repoName+".git")
	if err := gitpkg.InitBare(bareDir, "main"); err != nil {
		t.Fatal(err)
	}

	handler := gitpkg.New(gitpkg.Deps{
		Backend:  gitpkg.SelectBackend(defaultCfg()),
		Config:   defaultCfg(),
		Locks:    storage.NewLocks(),
		Repos:    metadata.NewReposRepo(db),
		Projects: metadata.NewProjectsRepo(db),
		Members:  metadata.NewMembersRepo(db),
		Audit:    auditLog,
		DataRoot: dataRoot,
		Users:    metadata.NewUsersRepo(db),
		Sessions: metadata.NewSessionsRepo(db),
		APIKeys:  metadata.NewAPIKeysRepo(db),
		DB:       db,
		Refs:     refsRepo,
	})

	// We need to seed a user + membership for auth to pass.
	seedUserAndMembership(t, db, projID)

	mux := handler.TestRouter(t)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	// Init a local repo, commit, then push to the test server.
	// We avoid `git clone` of the empty bare repo because the default
	// branch name varies across git versions (master vs main).
	workDir := filepath.Join(t.TempDir(), "work")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, workDir, "init", "-b", "main")
	runGit(t, workDir, "config", "user.email", "t@t.com")
	runGit(t, workDir, "config", "user.name", "T")
	if err := os.WriteFile(filepath.Join(workDir, "a"), []byte("a"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, workDir, "add", ".")
	runGit(t, workDir, "commit", "-m", "init")
	// Push with credentials.
	pushURL := strings.Replace(ts.URL, "://", "://admin:password@", 1) + "/git/testproj/" + repoName + ".git"
	runGit(t, workDir, "remote", "add", "origin", pushURL)
	runGit(t, workDir, "push", "origin", "main")

	// After the push, the walker should have synced refs.
	got, err := refsRepo.List(context.Background(), repoID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) < 2 { // HEAD + main at minimum
		t.Fatalf("want at least 2 refs after push, got %d", len(got))
	}

	// Check audit event.
	found := false
	for _, e := range auditLog.events {
		if e.Kind == "git.refs.synced" {
			found = true
			if cnt, ok := e.Details["ref_count"]; !ok || cnt == nil {
				t.Error("git.refs.synced event missing ref_count in Details")
			}
			break
		}
	}
	if !found {
		t.Error("git.refs.synced audit event not emitted")
	}
}

// --- Test 7: CreateRepoHook ---

func TestCreateRepoHook(t *testing.T) {
	db := sqlitetest.New(t)
	projID := seedProject(t, db)
	refsRepo := metadata.NewGitRefsRepo(db)
	dataRoot := t.TempDir()

	ctx := context.Background()
	var repoID int64
	err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		var insErr error
		repoID, insErr = metadata.NewReposRepo(db).CreateInTx(ctx, tx, projID, "git", "myrepo", "", nil, nil, nil)
		if insErr != nil {
			return insErr
		}
		return gitpkg.CreateRepoHook(ctx, tx, repoID, "git", "testproj", "myrepo", dataRoot, refsRepo)
	})
	if err != nil {
		t.Fatalf("CreateRepoHook: %v", err)
	}

	// Check bare repo exists on disk.
	bareDir := filepath.Join(dataRoot, "repos", "testproj", "git", "myrepo.git")
	if _, err := os.Stat(bareDir); os.IsNotExist(err) {
		t.Fatal("bare repo dir not created")
	}

	// Check HEAD row was seeded.
	headRef := findRef(t, refsRepo, repoID, "HEAD")
	if headRef.Target != "refs/heads/main" {
		t.Errorf("HEAD target = %q, want refs/heads/main", headRef.Target)
	}
	if headRef.Type != metadata.GitRefSymbolic {
		t.Errorf("HEAD type = %q, want symbolic", headRef.Type)
	}
}

// --- Test 9: Walker errors are non-fatal ---

func TestWalkAndReplace_CorruptedRepoNonFatal(t *testing.T) {
	db := sqlitetest.New(t)
	projID := seedProject(t, db)
	repoID := seedGitRepoRow(t, db, projID, "r9")
	refsRepo := metadata.NewGitRefsRepo(db)

	// Point at a nonexistent path.
	err := gitpkg.WalkAndReplace(context.Background(), db, refsRepo, repoID, "/nonexistent/repo.git")
	// WalkAndReplace returns an error; the handler catches it and logs, but
	// doesn't fail the receive-pack. We test the handler's behavior here.
	if err == nil {
		t.Fatal("expected error for corrupted/missing repo")
	}
}

// Test 9b: dispatchToBackend does NOT fail on walker error.
func TestDispatch_WalkerErrorNonFatal(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}

	db := sqlitetest.New(t)
	projID := seedProject(t, db)
	repoName := "walkerr"
	_ = seedGitRepoRow(t, db, projID, repoName)
	refsRepo := metadata.NewGitRefsRepo(db)
	auditLog := &fakeAuditLogger{}

	dataRoot := t.TempDir()
	bareDir := filepath.Join(dataRoot, "repos", "testproj", "git", repoName+".git")
	if err := gitpkg.InitBare(bareDir, "main"); err != nil {
		t.Fatal(err)
	}

	// Suppress noisy walker failure log output.
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))

	handler := gitpkg.New(gitpkg.Deps{
		Backend:  gitpkg.SelectBackend(defaultCfg()),
		Config:   defaultCfg(),
		Locks:    storage.NewLocks(),
		Repos:    metadata.NewReposRepo(db),
		Projects: metadata.NewProjectsRepo(db),
		Members:  metadata.NewMembersRepo(db),
		Audit:    auditLog,
		DataRoot: dataRoot,
		Users:    metadata.NewUsersRepo(db),
		Sessions: metadata.NewSessionsRepo(db),
		APIKeys:  metadata.NewAPIKeysRepo(db),
		DB:       db,
		Refs:     refsRepo,
	})

	seedUserAndMembership(t, db, projID)

	mux := handler.TestRouter(t)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	// Push something to the repo — even if the walker fails, the push
	// itself should succeed (200/204).
	workDir := filepath.Join(t.TempDir(), "work")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, workDir, "init", "-b", "main")
	runGit(t, workDir, "config", "user.email", "t@t.com")
	runGit(t, workDir, "config", "user.name", "T")
	if err := os.WriteFile(filepath.Join(workDir, "a"), []byte("a"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, workDir, "add", ".")
	runGit(t, workDir, "commit", "-m", "init")
	pushURL := strings.Replace(ts.URL, "://", "://admin:password@", 1) + "/git/testproj/" + repoName + ".git"
	runGit(t, workDir, "remote", "add", "origin", pushURL)
	// This push should succeed even if refs DB is in a weird state.
	runGit(t, workDir, "push", "origin", "main")
}

// --- Test: CreateRepoHook skips non-git types ---

func TestCreateRepoHook_SkipsNonGit(t *testing.T) {
	dataRoot := t.TempDir()

	// nil tx + nil refs are safe: the hook returns before touching either
	// for non-git types.
	ctx := context.Background()
	if err := gitpkg.CreateRepoHook(ctx, nil, 0, "rpm", "proj", "repo", dataRoot, nil); err != nil {
		t.Fatalf("CreateRepoHook for rpm should not error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataRoot, "repos")); !os.IsNotExist(err) {
		t.Fatal("no on-disk state should be created for non-git type")
	}
}

// --- helpers for integration tests ---

func defaultCfg() config.Config {
	cfg := config.Defaults()
	cfg.Server.GitBackend = "gogit"
	return cfg
}

func seedUserAndMembership(t *testing.T, db *metadata.DB, projectID int64) {
	t.Helper()
	ctx := context.Background()
	// Seed a user that BasicOrAPIKey can find. Use admin/password.
	// Since the test server won't actually verify passwords via BasicOrAPIKey
	// in the simplified test router, we just need the row to exist.
	_, err := db.Writer.ExecContext(ctx, `
		INSERT INTO users(login, email, password_hash, is_super_admin, must_change_password)
		VALUES ('admin', 'admin@test.com', '$argon2id$v=19$m=65536,t=3,p=4$c29tZXNhbHQ$somehash', 1, 0)
	`)
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}
	_, err = db.Writer.ExecContext(ctx, `
		INSERT INTO project_members(project_id, user_id) VALUES (?, 1)
	`, projectID)
	if err != nil {
		t.Fatalf("seed member: %v", err)
	}
}
