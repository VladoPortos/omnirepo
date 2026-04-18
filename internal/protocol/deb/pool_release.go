// Package deb — W-03 walkthrough micro-fix: Release-aware pool-path
// resolution. Replaces the naive filename-inference heuristic in
// relPoolPath with a Release-file-first reader that honors whatever
// Components line a non-default-layout repo declares, falling back to
// the legacy "pool/main/<initial>/<pkg>/<filename>" shape when the
// Release file is missing, malformed, or declares a traversal-unsafe
// component name.
package deb

import (
	"bufio"
	"bytes"
	"net/mail"
	"os"
	"path/filepath"
	"strings"
)

// ResolvePoolPath returns the canonical pool-relative path for a DEB
// package filename in the given repo and suite.
//
// Behaviour (W-03):
//
//  1. Attempt to read <repoRoot>/<project>/deb/<repo>/dists/<suite>/Release
//     and honour its first-listed Components: entry.
//  2. If the Release file is absent, empty, unparseable, missing the
//     Components: header, or declares a component containing "/" or ".."
//     (T-07-06-01 traversal mitigation), fall back to the legacy
//     filename-inference path `pool/main/<initial>/<pkg>/<filename>`.
//
// This keeps default-layout repos round-tripping cleanly while letting
// non-default layouts (custom components, explicit arch listings) preserve
// their shape across OmniRepo-mediated sync.
//
// ctrl may be nil; in that case the package name defaults to "x" to
// preserve the legacy relPoolPath contract for pre-parse entries.
func ResolvePoolPath(repoRoot, projectName, repoName, suite, filename string, ctrl *Control) string {
	pkg := "x"
	if ctrl != nil && ctrl.Package != "" {
		pkg = ctrl.Package
	}
	initial := pkg[:1]

	component := "main"
	releasePath := filepath.Join(repoRoot, projectName, "deb", repoName, "dists", suite, "Release")
	if data, err := os.ReadFile(releasePath); err == nil {
		if first := extractFirstComponent(data); first != "" && isSafeComponent(first) {
			component = first
		}
	}

	return "pool/" + component + "/" + initial + "/" + pkg + "/" + filename
}

// extractFirstComponent parses a Debian Release file (RFC 822 headers) and
// returns the first entry in the Components: line (e.g. "main" from
// "Components: main contrib non-free"). Returns "" if the field is absent,
// empty, or the file cannot be parsed.
//
// Release files conventionally terminate the header block with a blank
// line followed by either an empty body or a signature block; some
// implementations emit only headers without the trailing blank line, so
// we synthesize one when it's missing to satisfy net/mail.ReadMessage.
func extractFirstComponent(data []byte) string {
	buf := bytes.NewBuffer(nil)
	buf.Write(data)
	if !bytes.Contains(data, []byte("\n\n")) {
		buf.WriteString("\n")
	}
	msg, err := mail.ReadMessage(bufio.NewReader(buf))
	if err != nil {
		return ""
	}
	comp := msg.Header.Get("Components")
	if comp == "" {
		return ""
	}
	fields := strings.Fields(comp)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// isSafeComponent returns true if s is a valid Debian component name that
// cannot escape the pool/ tree. T-07-06-01 mitigation: reject any value
// containing "/", "..", or a NUL byte, and any non-empty value longer than
// 64 chars (defensive upper bound — real component names are short).
func isSafeComponent(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	if strings.Contains(s, "/") || strings.Contains(s, "..") || strings.ContainsRune(s, 0) {
		return false
	}
	return true
}
