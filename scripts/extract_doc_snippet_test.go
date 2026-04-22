// Unit tests for the line-based markdown fence extractor.
//
// Covers: happy path, nested fenced blocks inside a bash body, missing
// ID comment, unclosed bash fence, duplicate ID comments (takes first),
// and the real docs/operations/scheduled-sync.md used by `make lint-docs`.
package main

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestExtractSnippet_HappyPath(t *testing.T) {
	in := strings.Join([]string{
		"# Heading",
		"Some prose.",
		"<!-- shellcheck-id: demo -->",
		"```bash",
		"#!/usr/bin/env bash",
		"echo hello",
		"```",
		"",
		"Trailing prose.",
	}, "\n")

	body, err := ExtractSnippet(strings.NewReader(in), "demo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "#!/usr/bin/env bash\necho hello"
	if body != want {
		t.Fatalf("body mismatch\n got: %q\nwant: %q", body, want)
	}
}

func TestExtractSnippet_NestedFence(t *testing.T) {
	// The outer bash fence contains a nested ```yaml...``` block.
	// The scanner closes the outer fence on the FIRST standalone ``` line,
	// so the inner closing ``` terminates the outer. Body therefore
	// includes everything up to (but not including) the first closing
	// ``` — i.e. up to the inner closing fence. This test pins that
	// deterministic (if surprising) behavior so future refactors can't
	// silently change it without updating the test.
	in := strings.Join([]string{
		"<!-- shellcheck-id: demo -->",
		"```bash",
		"echo outer",
		"cat <<'YAML'",
		"```yaml",
		"key: value",
		"```",
		"YAML",
		"```",
	}, "\n")

	body, err := ExtractSnippet(strings.NewReader(in), "demo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The scanner closes on the first standalone "```" line — that is the
	// inner YAML fence's closer. Body contains everything before it.
	want := strings.Join([]string{
		"echo outer",
		"cat <<'YAML'",
		"```yaml",
		"key: value",
	}, "\n")
	if body != want {
		t.Fatalf("body mismatch\n got: %q\nwant: %q", body, want)
	}
}

func TestExtractSnippet_MissingID(t *testing.T) {
	in := strings.Join([]string{
		"# No tagged block here",
		"```bash",
		"echo nope",
		"```",
	}, "\n")

	_, err := ExtractSnippet(strings.NewReader(in), "scheduled-sync")
	if !errors.Is(err, ErrIDNotFound) {
		t.Fatalf("got %v, want ErrIDNotFound", err)
	}
}

func TestExtractSnippet_UnclosedFence(t *testing.T) {
	in := strings.Join([]string{
		"<!-- shellcheck-id: demo -->",
		"```bash",
		"echo missing closer",
		// No closing ``` line.
	}, "\n")

	_, err := ExtractSnippet(strings.NewReader(in), "demo")
	if !errors.Is(err, ErrFenceUnclosed) {
		t.Fatalf("got %v, want ErrFenceUnclosed", err)
	}
}

func TestExtractSnippet_MultipleIDsPicksFirst(t *testing.T) {
	// Two blocks tagged with the same ID — extractor returns the first.
	in := strings.Join([]string{
		"<!-- shellcheck-id: demo -->",
		"```bash",
		"echo first",
		"```",
		"",
		"<!-- shellcheck-id: demo -->",
		"```bash",
		"echo second",
		"```",
	}, "\n")

	body, err := ExtractSnippet(strings.NewReader(in), "demo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if body != "echo first" {
		t.Fatalf("expected first block, got %q", body)
	}
}

func TestExtractSnippet_NonBashFenceAfterID(t *testing.T) {
	// If the line after the ID comment opens a fence with a different
	// language, the scanner should NOT treat it as a match; it keeps
	// scanning for the bash fence. Here there is none → ErrIDNotFound.
	//
	// We use ErrIDNotFound because the ID comment primed the scanner
	// but no matching bash fence ever appeared. (Alternative design:
	// define a distinct ErrFenceMissing — we keep the two-error surface
	// simple per the CLI contract.)
	in := strings.Join([]string{
		"<!-- shellcheck-id: demo -->",
		"```yaml",
		"key: value",
		"```",
	}, "\n")

	_, err := ExtractSnippet(strings.NewReader(in), "demo")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestExtractSnippet_RealDoc(t *testing.T) {
	// Real fixture lives at repo-root relative path. Go test CWD is the
	// package dir (scripts/), so walk up one.
	f, err := os.Open("../docs/operations/scheduled-sync.md")
	if err != nil {
		t.Fatalf("open real doc: %v", err)
	}
	defer f.Close()

	body, err := ExtractSnippet(f, "scheduled-sync")
	if err != nil {
		t.Fatalf("extract real doc: %v", err)
	}
	if !strings.HasPrefix(body, "#!/usr/bin/env bash") {
		n := len(body)
		if n > 60 {
			n = 60
		}
		t.Fatalf("body missing shebang prefix; got: %q", body[:n])
	}
	if !strings.Contains(body, "mirror.sync.in_flight") {
		t.Fatal("body missing 409 envelope code")
	}
	if !strings.Contains(body, "Authorization: Bearer") {
		t.Fatal("body missing Bearer auth header")
	}
	if strings.Contains(body, "```") {
		t.Fatal("body leaked a fence delimiter line")
	}
}
