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
//  2. If Release is absent, try the clearsigned <suite>/InRelease variant
//     (modern Debian publishers emit InRelease only). The PGP clearsign
//     wrapper is stripped before RFC822 parsing.
//  3. If neither file is available, empty, unparseable, missing the
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
	distsDir := filepath.Join(repoRoot, projectName, "deb", repoName, "dists", suite)
	for _, name := range []string{"Release", "InRelease"} {
		data, err := os.ReadFile(filepath.Join(distsDir, name))
		if err != nil {
			continue
		}
		if name == "InRelease" {
			data = stripClearsign(data)
		}
		if first := extractFirstComponent(data); first != "" && isSafeComponent(first) {
			component = first
			break
		}
	}

	return "pool/" + component + "/" + initial + "/" + pkg + "/" + filename
}

// stripClearsign extracts the RFC822 header block from a PGP clearsigned
// message (as emitted by `apt-ftparchive release | gpg --clearsign`). It
// removes the "-----BEGIN PGP SIGNED MESSAGE-----" preamble + optional
// Hash: headers, and the trailing "-----BEGIN PGP SIGNATURE-----" block.
// If the input is not clearsigned, it is returned unchanged.
func stripClearsign(data []byte) []byte {
	const preamble = "-----BEGIN PGP SIGNED MESSAGE-----"
	const sigStart = "\n-----BEGIN PGP SIGNATURE-----"
	idx := bytes.Index(data, []byte(preamble))
	if idx < 0 {
		return data
	}
	// Skip preamble line.
	rest := data[idx+len(preamble):]
	if nl := bytes.IndexByte(rest, '\n'); nl >= 0 {
		rest = rest[nl+1:]
	}
	// Skip Hash: / Comment: header lines until a blank line.
	if blank := bytes.Index(rest, []byte("\n\n")); blank >= 0 {
		rest = rest[blank+2:]
	}
	// Trim at the signature block.
	if end := bytes.Index(rest, []byte(sigStart)); end >= 0 {
		rest = rest[:end]
	}
	return rest
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
