// Package docs_test — structural assertions over
// docs/operations/scheduled-sync.md.
//
// The doc IS the test fixture for the shellcheck extractor:
// drift in the shellcheck-id comment, the bash fence position, the
// Bearer auth header, the 409 envelope code, or the K8s manifest
// knobs all get caught here instead of at lint-docs time.
//
// Intentionally string-based: markdown parsing would over-engineer
// invariants that are one-line greps.
package docs_test

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// projectRoot walks up from the package's working dir until it finds
// go.mod. Mirrors the pattern used in test/bench/git/bench_test.go so
// the test runs the same way from `go test ./test/docs/...` regardless
// of caller cwd.
func projectRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find project root (go.mod)")
		}
		dir = parent
	}
}

// docPath resolves the absolute path to docs/operations/scheduled-sync.md.
func docPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(projectRoot(t), "docs", "operations", "scheduled-sync.md")
}

// readDoc returns the doc contents; fails the test if the file is
// missing (the doc is required to exist).
func readDoc(t *testing.T) string {
	t.Helper()
	p := docPath(t)
	b, err := os.ReadFile(p) // #nosec G304 -- path is a constant under the repo
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return string(b)
}

// TestScheduledSyncDocStructure asserts every invariant
// that reduces to a string-presence check against the doc body.
func TestScheduledSyncDocStructure(t *testing.T) {
	body := readDoc(t)

	// Required substrings, keyed by the invariant they back.
	cases := []struct {
		name string
		want string
	}{
		{"shellcheck-id comment", "<!-- shellcheck-id: scheduled-sync -->"},
		{"bash shebang", "#!/usr/bin/env bash"},
		{"set -euo pipefail", "set -euo pipefail"},
		{"Bearer auth header", "Authorization: Bearer"},
		{"409 envelope code", "mirror.sync.in_flight"},
		{"K8s CronJob apiVersion", "apiVersion: batch/v1"},
		{"K8s CronJob kind", "kind: CronJob"},
		{"concurrencyPolicy Forbid", "concurrencyPolicy: Forbid"},
		{"backoffLimit 0", "backoffLimit: 0"},
		{"curlimages/curl image", "curlimages/curl"},
		{"Secret-mounted key", "secretKeyRef:"},
		{"real upstream URL", "archive.ubuntu.com"},
		{"protocol substitution: rpm", "REPO_TYPE=rpm"},
		{"protocol substitution: pypi", "REPO_TYPE=pypi"},
		{"protocol substitution: helm", "REPO_TYPE=helm"},
		{"protocol substitution: docker", "REPO_TYPE=docker"},
		{"verified POST /sync path fragment", "/sync\""},
		{"verified GET /sync-jobs/ path fragment", "/sync-jobs/"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(body, tc.want) {
				t.Errorf("doc missing required substring %q (%s)", tc.want, tc.name)
			}
		})
	}
}

// TestScheduledSyncDoc_NoWrongAuthHeader guards against future drift
// toward X-API-Key — a header OmniRepo's middleware does NOT accept
// (verified absent from internal/auth/middleware/session_or_apikey.go).
func TestScheduledSyncDoc_NoWrongAuthHeader(t *testing.T) {
	body := readDoc(t)
	forbidden := []string{"X-API-Key", "X-Api-Key", "x-api-key"}
	for _, h := range forbidden {
		if strings.Contains(body, h) {
			t.Errorf("doc must not reference %q — OmniRepo only accepts Authorization: Bearer or Basic auth", h)
		}
	}
}

// TestScheduledSyncDoc_ExactlyOneShellcheckTag guards the shellcheck
// extractor, which assumes the ID comment is unique. Multiple tags
// would silently extract the wrong fence or none at all.
func TestScheduledSyncDoc_ExactlyOneShellcheckTag(t *testing.T) {
	body := readDoc(t)
	const tag = "<!-- shellcheck-id:"
	if got := strings.Count(body, tag); got != 1 {
		t.Errorf("expected exactly 1 %q comment, got %d", tag, got)
	}
}

// TestScheduledSyncDoc_BashFenceFollowsTag asserts the extractor's
// locality invariant: within 10 lines after the shellcheck-id comment,
// the bash fence opens. Prevents accidental reordering that would
// break the line-based extractor.
func TestScheduledSyncDoc_BashFenceFollowsTag(t *testing.T) {
	f, err := os.Open(docPath(t)) // #nosec G304 -- path is a constant under the repo
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = f.Close() }()

	sc := bufio.NewScanner(f)
	// Support long lines — the YAML inline args can exceed the default 64K.
	sc.Buffer(make([]byte, 0, 1024*1024), 1024*1024)

	const (
		tagLine   = "<!-- shellcheck-id: scheduled-sync -->"
		fenceLine = "```bash"
	)

	tagLineNo := -1
	fenceLineNo := -1
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := sc.Text()
		if tagLineNo < 0 && strings.TrimSpace(line) == tagLine {
			tagLineNo = lineNo
			continue
		}
		if tagLineNo > 0 && strings.TrimSpace(line) == fenceLine {
			fenceLineNo = lineNo
			break
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if tagLineNo < 0 {
		t.Fatalf("shellcheck-id tag not found")
	}
	if fenceLineNo < 0 {
		t.Fatalf("bash fence not found after shellcheck-id tag")
	}
	if delta := fenceLineNo - tagLineNo; delta < 1 || delta > 10 {
		t.Errorf("bash fence must open within 10 lines of shellcheck-id tag (tag=line %d, fence=line %d, delta=%d)",
			tagLineNo, fenceLineNo, delta)
	}
}
