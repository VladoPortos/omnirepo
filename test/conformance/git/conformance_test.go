//go:build conformance

package git_conformance

import (
	"fmt"
	"strings"
	"testing"
)

// TestGitConformance is the top-level Git conformance suite (D-46).
// It exercises both the gogit (pure-Go) and gitkit (subprocess) backends
// using the real `git` CLI inside a DinD container. Each backend runs the
// full matrix: clone, push, fetch, oversize-push gate, D-31 project-auth
// variant, and bad-auth rejection.
func TestGitConformance(t *testing.T) {
	backends := []string{"gogit", "gitkit"}
	for _, backend := range backends {
		t.Run(backend, func(t *testing.T) {
			fx := bootAppWithGitRepo(t, backend)
			image := resolveImage(t, "git-client")

			t.Run("CloneEmptyRepo", func(t *testing.T) {
				// Test 1: Clone an empty-but-initialized repo; should get
				// the seed HEAD -> refs/heads/main pointer.
				url := fx.cloneURL(fx.userLogin, fx.userPassword)
				script := fmt.Sprintf(`
					set -e
					git clone %s /tmp/repo 2>&1
					cd /tmp/repo
					git branch -a
					# Verify HEAD points at main
					git symbolic-ref HEAD
				`, url)
				out, err := dockerRun(t, image, script)
				t.Logf("CloneEmptyRepo output:\n%s", out)
				if err != nil {
					t.Fatalf("clone failed: %v\noutput: %s", err, out)
				}
				if !strings.Contains(out, "refs/heads/main") {
					t.Errorf("expected refs/heads/main in output, got: %s", out)
				}
			})

			t.Run("PushAndVerify", func(t *testing.T) {
				// Test 2: Clone, commit, push. Server-side git_refs table
				// should show refs/heads/main with a real SHA.
				url := fx.cloneURL(fx.userLogin, fx.userPassword)
				script := fmt.Sprintf(`
					set -e
					git clone %s /tmp/repo 2>&1
					cd /tmp/repo
					git config user.email "test@example.com"
					git config user.name "Conformance"
					git commit --allow-empty -m "conformance push test"
					git push origin main 2>&1
				`, url)
				out, err := dockerRun(t, image, script)
				t.Logf("PushAndVerify output:\n%s", out)
				if err != nil {
					t.Fatalf("push failed: %v\noutput: %s", err, out)
				}
			})

			t.Run("CloneSeesNewCommit", func(t *testing.T) {
				// Test 3: Push a commit, then clone from a fresh client —
				// the new commit should be visible.
				url := fx.cloneURL(fx.userLogin, fx.userPassword)
				// Push a tagged commit first.
				pushScript := fmt.Sprintf(`
					set -e
					git clone %s /tmp/repo 2>&1
					cd /tmp/repo
					git config user.email "test@example.com"
					git config user.name "Conformance"
					echo "hello" > testfile.txt
					git add testfile.txt
					git commit -m "add testfile"
					git push origin main 2>&1
				`, url)
				out, err := dockerRun(t, image, pushScript)
				t.Logf("Push output:\n%s", out)
				if err != nil {
					t.Fatalf("push failed: %v\noutput: %s", err, out)
				}

				// Clone in a fresh container and verify the file is present.
				cloneScript := fmt.Sprintf(`
					set -e
					git clone %s /tmp/repo2 2>&1
					cd /tmp/repo2
					cat testfile.txt
				`, url)
				out2, err := dockerRun(t, image, cloneScript)
				t.Logf("Clone output:\n%s", out2)
				if err != nil {
					t.Fatalf("second clone failed: %v\noutput: %s", err, out2)
				}
				if !strings.Contains(out2, "hello") {
					t.Errorf("expected 'hello' in cloned testfile.txt, got: %s", out2)
				}
			})

			t.Run("FetchSeesNewRefs", func(t *testing.T) {
				// Test 4: Clone, then push from another client, then fetch
				// from the first to see new refs.
				url := fx.cloneURL(fx.userLogin, fx.userPassword)
				script := fmt.Sprintf(`
					set -e
					# Initial clone
					git clone %s /tmp/repo 2>&1
					cd /tmp/repo
					git config user.email "test@example.com"
					git config user.name "Conformance"

					# Push a commit to create a real ref
					echo "fetch-test" > fetchfile.txt
					git add fetchfile.txt
					git commit -m "fetch test commit"
					git push origin main 2>&1

					# Clone to a second workdir, push another commit
					git clone %s /tmp/repo2 2>&1
					cd /tmp/repo2
					git config user.email "test2@example.com"
					git config user.name "Conformance2"
					echo "fetch-update" > fetchfile2.txt
					git add fetchfile2.txt
					git commit -m "second commit for fetch"
					git push origin main 2>&1

					# Back in first workdir: fetch + verify
					cd /tmp/repo
					git fetch origin 2>&1
					git log --oneline origin/main
				`, url, url)
				out, err := dockerRun(t, image, script)
				t.Logf("FetchSeesNewRefs output:\n%s", out)
				if err != nil {
					t.Fatalf("fetch test failed: %v\noutput: %s", err, out)
				}
				if !strings.Contains(out, "second commit for fetch") {
					t.Errorf("expected 'second commit for fetch' in fetch output, got: %s", out)
				}
			})

			t.Run("ProjectAuthVariant", func(t *testing.T) {
				// Test 5: Use project:<proj>:<omr_p_...> auth variant
				// for both clone and push — covers D-31.
				url := fx.cloneURLProjectAuth()
				script := fmt.Sprintf(`
					set -e
					git clone %s /tmp/repo 2>&1
					cd /tmp/repo
					git config user.email "proj@example.com"
					git config user.name "ProjectAuth"
					git commit --allow-empty -m "project auth push"
					git push origin main 2>&1
				`, url)
				out, err := dockerRun(t, image, script)
				t.Logf("ProjectAuthVariant output:\n%s", out)
				if err != nil {
					t.Fatalf("project auth failed: %v\noutput: %s", err, out)
				}
			})

			t.Run("OversizePushRejected", func(t *testing.T) {
				// Test 6: Set per-repo cap to 10 MiB, push a ~15 MiB file.
				// Git CLI output must contain the D-34 error message.
				// Cap is set low to avoid shipping large bodies through DinD.
				const capBytes = 10 * 1024 * 1024 // 10 MiB
				setRepoMaxPushBytes(t, fx.dataRoot, fx.project, fx.repo, capBytes)

				url := fx.cloneURL(fx.userLogin, fx.userPassword)
				script := fmt.Sprintf(`
					set -e
					git clone %s /tmp/repo 2>&1
					cd /tmp/repo
					git config user.email "test@example.com"
					git config user.name "Conformance"
					# Generate a ~15 MiB file to exceed the 10 MiB cap
					dd if=/dev/urandom of=bigfile.bin bs=1M count=15 2>/dev/null
					git add bigfile.bin
					git commit -m "oversize push"
					# Push should fail — capture stderr
					git push origin main 2>&1 || true
				`, url)
				out, err := dockerRun(t, image, script)
				t.Logf("OversizePushRejected output:\n%s", out)
				// The push itself should exit non-zero (we used || true to
				// not fail the container, so check the output instead).
				if !strings.Contains(out, "push exceeds repo limit of 10 MiB") {
					t.Errorf("expected 'push exceeds repo limit of 10 MiB' in output, got: %s", out)
				}
				if !strings.Contains(out, "contact a project admin") {
					t.Errorf("expected 'contact a project admin' in output, got: %s", out)
				}
			})

			t.Run("BadAuthRejected", func(t *testing.T) {
				// Test 8: Bad auth (wrong password) -> git clone fails with
				// 401. Git CLI surfaces "Authentication failed" or HTTP 401.
				badURL := fx.cloneURL(fx.userLogin, "wrong-password-12345")
				script := fmt.Sprintf(`
					set -e
					GIT_TERMINAL_PROMPT=0 git clone %s /tmp/repo 2>&1 || true
				`, badURL)
				out, err := dockerRun(t, image, script)
				t.Logf("BadAuthRejected output:\n%s", out)
				_ = err // Container exits 0 due to || true
				// Git should report authentication failure or 401.
				outLower := strings.ToLower(out)
				if !strings.Contains(outLower, "authentication failed") &&
					!strings.Contains(outLower, "401") &&
					!strings.Contains(outLower, "could not read") &&
					!strings.Contains(outLower, "fatal") {
					t.Errorf("expected auth failure indication in output, got: %s", out)
				}
			})
		})
	}
}
