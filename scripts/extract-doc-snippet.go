// Command extract-doc-snippet extracts a fenced bash code block tagged
// by an HTML comment `<!-- shellcheck-id: <id> -->` from a markdown
// file and writes the body (without fence lines) to stdout.
//
// Source of truth: phase 10 CONTEXT D-22 and plan 10-05 which embeds
// the canonical `scheduled-sync` fixture. Used by `make lint-docs`
// (plan 10-06 / CRONDOCS-04) to pipe the documented bash snippet into
// shellcheck, so the doc IS the lint fixture — no sidecar script.
//
// The scanner is deliberately line-based (per Pitfall 10.7) rather
// than a multi-line regex. Regex approaches either miss nested fences
// (greedy `.*` across newlines) or require `(?s)` flags that turn out
// to interact poorly with markdown source files that contain their
// own ``` characters inside heredocs. A line-based state machine is
// short, trivially testable, and deterministic.
//
// Exit codes:
//
//	0  success
//	1  ID comment not found
//	2  opening fence never closed
//	3  I/O or flag error
//
// Usage:
//
//	go run -mod=vendor scripts/extract-doc-snippet.go \
//	  --id scheduled-sync \
//	  --file docs/operations/scheduled-sync.md
package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

// ErrIDNotFound indicates the markdown did not contain
// `<!-- shellcheck-id: <id> -->` followed by an opening ```bash fence.
// This covers both "no ID comment at all" and "ID comment present but
// the immediately-following scanned region never opened a bash fence
// before EOF" — they are the same failure from the caller's point of
// view (the fixture is missing).
var ErrIDNotFound = errors.New("shellcheck-id comment not found or not followed by a bash fence")

// ErrFenceUnclosed indicates the scanner found the ID comment and
// opened a ```bash fence but reached EOF without ever seeing a lone
// closing ``` line.
var ErrFenceUnclosed = errors.New("opening bash fence never closed")

// ExtractSnippet reads markdown from r and returns the body of the
// bash fenced block tagged by an HTML comment of the form
// `<!-- shellcheck-id: <id> -->` placed on its own line immediately
// before a ```bash opening fence.
//
// The scanner uses three states:
//
//	primed = "have we seen the ID comment?"
//	inside = "are we currently collecting lines inside a bash fence?"
//	done   = "have we collected a full block and returned?"
//
// A block closes on the first line whose trimmed content equals "```".
// If the markdown nests another fence inside the bash body (rare —
// plan 10-05 deliberately avoids it), that nested closer will close
// the outer fence too. This is documented behavior (see the nested
// fence test) because the alternative — tracking fence nesting by
// language — is substantially more complex and serves no real doc
// whose contents the extractor would legitimately index.
//
// Returns the body joined by '\n' on success, or a sentinel error on
// failure. Does NOT write to stdout; that is the CLI's responsibility.
func ExtractSnippet(r io.Reader, id string) (string, error) {
	wantComment := "<!-- shellcheck-id: " + id + " -->"

	scanner := bufio.NewScanner(r)
	// Defensive buffer for long doc lines (shebang+comment lines in
	// the real fixture are <120 chars, but heredoc bodies in other
	// future snippets could be larger). 1 MiB max line is absurdly
	// generous for markdown but cheap.
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	var (
		primed bool
		inside bool
		body   []string
	)

	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)

		switch {
		case !primed && trimmed == wantComment:
			primed = true

		case primed && !inside && strings.HasPrefix(trimmed, "```bash"):
			inside = true

		case inside && trimmed == "```":
			return strings.Join(body, "\n"), nil

		case inside:
			body = append(body, line)
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read markdown: %w", err)
	}

	switch {
	case inside:
		// Opened a fence but never closed it before EOF.
		return "", ErrFenceUnclosed
	default:
		// Either never saw the ID comment, or saw it but never saw a
		// following ```bash fence. Both are "fixture missing" from the
		// caller's point of view.
		return "", ErrIDNotFound
	}
}

func main() {
	// Flags declared on a dedicated FlagSet so we can write errors to
	// stderr and exit with the documented code without os.Exit(2) from
	// the global flag package's default behavior.
	fs := flag.NewFlagSet("extract-doc-snippet", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	id := fs.String("id", "", "shellcheck-id value to extract (required)")
	path := fs.String("file", "", "path to the markdown file (required)")

	if err := fs.Parse(os.Args[1:]); err != nil {
		// flag.ContinueOnError already printed usage to stderr.
		os.Exit(3)
	}
	if *id == "" || *path == "" {
		fmt.Fprintln(os.Stderr, "error: --id and --file are required")
		fs.Usage()
		os.Exit(3)
	}

	f, err := os.Open(*path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open %s: %v\n", *path, err)
		os.Exit(3)
	}
	defer f.Close()

	body, err := ExtractSnippet(f, *id)
	switch {
	case errors.Is(err, ErrIDNotFound):
		fmt.Fprintf(os.Stderr, "shellcheck-id %q not found in %s\n", *id, *path)
		os.Exit(1)
	case errors.Is(err, ErrFenceUnclosed):
		fmt.Fprintf(os.Stderr, "unclosed bash fence after shellcheck-id %q in %s\n", *id, *path)
		os.Exit(2)
	case err != nil:
		fmt.Fprintf(os.Stderr, "extract %s: %v\n", *path, err)
		os.Exit(3)
	}

	// Preserve a trailing newline so shellcheck (and `bash -n`) see a
	// POSIX-conformant text file, not a last-line-without-newline blob.
	if _, err := io.WriteString(os.Stdout, body+"\n"); err != nil {
		fmt.Fprintf(os.Stderr, "write stdout: %v\n", err)
		os.Exit(3)
	}
}
