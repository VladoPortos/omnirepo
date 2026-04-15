//go:build spike

package spike

import (
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"
)

// TestGoGitV6EndToEnd runs four real git(1) CLI invocations against a chi
// router that mounts v6 server primitives:
//
//  1. git clone
//  2. commit + git push
//  3. git fetch
//  4. second git clone
//
// All four MUST exit 0 for the spike to pass (D-38 acceptance).
func TestGoGitV6EndToEnd(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git CLI not available in PATH")
	}

	tmp := t.TempDir()
	bareDir := filepath.Join(tmp, "dxc", "infra.git")
	if err := os.MkdirAll(filepath.Dir(bareDir), 0o750); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "", "git", "init", "--bare", "--initial-branch=main", bareDir)

	r := chi.NewRouter()
	loader := NewSimpleLoader(map[string]string{"/dxc/infra.git": bareDir})
	_ = MountSpike(r, loader)

	srv := httptest.NewServer(r)
	defer srv.Close()
	repoURL := srv.URL + "/git/dxc/infra.git"

	// 1. git clone
	clone1 := filepath.Join(tmp, "clone1")
	mustRun(t, tmp, "git", "clone", repoURL, clone1)

	// 2. commit + git push
	mustRun(t, clone1, "git", "config", "user.email", "test@example.com")
	mustRun(t, clone1, "git", "config", "user.name", "Spike")
	if err := os.WriteFile(filepath.Join(clone1, "README.md"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, clone1, "git", "add", "README.md")
	mustRun(t, clone1, "git", "commit", "-m", "initial")
	mustRun(t, clone1, "git", "push", "origin", "HEAD:refs/heads/main")

	// 3. git fetch (should be up-to-date)
	mustRun(t, clone1, "git", "fetch", "origin")

	// 4. second git clone — the important end-to-end proof that the push
	//    actually landed and can be served back.
	clone2 := filepath.Join(tmp, "clone2")
	mustRun(t, tmp, "git", "clone", repoURL, clone2)
	b, err := os.ReadFile(filepath.Join(clone2, "README.md"))
	if err != nil {
		t.Fatalf("read README.md from clone2: %v", err)
	}
	if string(b) != "hello" {
		t.Fatalf("content mismatch: got %q want %q", string(b), "hello")
	}
}

func mustRun(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	// Silence git CLI's global config pickup so host environment cannot
	// affect the test (e.g. user.email lookups).
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v failed: %v\n--- output ---\n%s", name, args, err, string(out))
	}
}
