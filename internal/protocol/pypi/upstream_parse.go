// Package pypi — upstream parser.
//
// Parses an upstream PyPI Simple repository (PEP 503 HTML or PEP 691 JSON)
// into a flat list of UpstreamFile entries. The sync handler iterates these
// to fetch missing wheels/sdists.
//
// PEP 691 (JSON Simple) is preferred via Accept content negotiation. If the
// upstream returns a non-JSON response, falls back to PEP 503 HTML parsing
// using a tightly-constrained regex (the simple HTML format guarantees
// `<a href="...#sha256=hex">filename</a>` shape so a regex avoids pulling
// in golang.org/x/net/html as a new dependency).
package pypi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/vladoportos/omnirepo/internal/protocol/upstreamfetch"
	"github.com/vladoportos/omnirepo/internal/streamio"
)

// maxSimpleIndexBytes caps the /simple/ index page body. Test-overridable
// (var, not const) so cap+1 oversized-upstream regression guards can run
// without serving multi-MiB bodies. Production callers do not mutate it.
var maxSimpleIndexBytes int64 = 64 * 1024 * 1024

// maxProjectPageBytes caps the /simple/<project>/ page body. Same
// test-overridable rationale as maxSimpleIndexBytes.
var maxProjectPageBytes int64 = 16 * 1024 * 1024

// UpstreamFile is one file entry parsed from an upstream PyPI Simple
// project page. Path holds the absolute or upstream-relative URL the sync
// handler should fetch; SHA256 is the bare hex digest (no "sha256:"
// prefix) when the upstream supplied one.
type UpstreamFile struct {
	Filename       string
	URL            string
	SHA256         string
	RequiresPython string
	Size           int64
}

// AuthCreds carries optional Basic / Bearer credentials threaded into the
// outbound request. Alias of the shared upstreamfetch.Creds.
type AuthCreds = upstreamfetch.Creds

// SyncFilter narrows the per-project file enumeration. Names are matched
// case-insensitively against the PEP 503 normalized project name; Globs
// match the candidate filename via filepath.Match. Both empty = no filter.
type SyncFilter struct {
	Names []string
	Globs []string
}

// pep691Project mirrors the JSON shape of PEP 691 /simple/<project>/.
type pep691Project struct {
	Meta  map[string]any `json:"meta"`
	Name  string         `json:"name"`
	Files []pep691File   `json:"files"`
}

// pep691File mirrors one file entry inside a pep691Project response.
type pep691File struct {
	Filename       string            `json:"filename"`
	URL            string            `json:"url"`
	Hashes         map[string]string `json:"hashes"`
	RequiresPython string            `json:"requires-python"`
	Size           int64             `json:"size"`
	Yanked         any               `json:"yanked,omitempty"`
}

// pep691Index mirrors the JSON shape of PEP 691 /simple/.
type pep691Index struct {
	Meta     map[string]any   `json:"meta"`
	Projects []pep691IndexEnt `json:"projects"`
}

type pep691IndexEnt struct {
	Name string `json:"name"`
}

// htmlFileRE matches `<a href="<url>#sha256=<hex>" data-requires-python="...">filename</a>`.
// PEP 503 only mandates the href-with-fragment shape; data-requires-python
// is optional. Filename comes from the inner text.
var htmlFileRE = regexp.MustCompile(`(?is)<a\s+[^>]*href="([^"#]+)#sha256=([0-9a-fA-F]+)"[^>]*>([^<]+)</a>`)

// htmlRequiresPythonRE extracts data-requires-python when present.
var htmlRequiresPythonRE = regexp.MustCompile(`(?i)data-requires-python="([^"]*)"`)

// htmlAnchorRE matches the project list at /simple/.
var htmlAnchorRE = regexp.MustCompile(`(?is)<a\s+[^>]*href="[^"]+"[^>]*>([^<]+)</a>`)

// simpleBaseURL normalizes an operator-supplied upstream URL to the
// canonical base that sits ABOVE the PEP 503 `/simple/` root, so callers
// can then append `/simple/...` deterministically. Accepts either form:
//
//	https://pypi.org              → https://pypi.org
//	https://pypi.org/             → https://pypi.org
//	https://pypi.org/simple       → https://pypi.org
//	https://pypi.org/simple/      → https://pypi.org
//
// The UI placeholder in MirrorConfigSection and PEP 503 itself both
// describe a PyPI mirror by its Simple-index URL ending in `/simple/`.
// Operators naturally enter `https://pypi.org/simple/`. Before this
// normalization the handler appended `/simple/` unconditionally, yielding
// `https://pypi.org/simple/simple/` — which pypi.org answers as the
// (nonexistent) project literally named "simple" with an empty `files`
// list. Sync then completed "done" with zero files, silently — same class
// as the APT `filter.Suites` drift (commit f11ff39): wire shape drifts
// from handler expectation, unit tests bypass the REST/upstream boundary
// and miss it.
func simpleBaseURL(upstream string) string {
	return strings.TrimSuffix(strings.TrimRight(upstream, "/"), "/simple")
}

// ParseUpstreamSimpleIndex fetches <upstream>/simple/ and returns the
// list of normalized project names. JSON is preferred via Accept; on
// 406 / non-JSON content-type the response is reparsed as HTML.
func ParseUpstreamSimpleIndex(ctx context.Context, client *http.Client, upstream string, creds AuthCreds) ([]string, error) {
	if client == nil {
		client = http.DefaultClient
	}
	indexURL := simpleBaseURL(upstream) + "/simple/"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, indexURL, nil)
	if err != nil {
		return nil, fmt.Errorf("pypi upstream: build req: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.pypi.simple.v1+json, text/html;q=0.5")
	upstreamfetch.ApplyCreds(req, creds)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("pypi upstream: get %s: %w", indexURL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("pypi upstream: %s -> %d", indexURL, resp.StatusCode)
	}
	// Fail-explicit on cap+1 instead of the prior silent-truncation idiom
	// (full-buffer read through a LimitReader).
	body, err := streamio.ReadAllLimited(resp.Body, maxSimpleIndexBytes, streamio.ErrMetadataTooLarge)
	if err != nil {
		return nil, fmt.Errorf("pypi upstream: read body: %w", err)
	}
	if isJSON(resp.Header.Get("Content-Type")) {
		var idx pep691Index
		if jerr := json.Unmarshal(body, &idx); jerr == nil {
			out := make([]string, 0, len(idx.Projects))
			for _, p := range idx.Projects {
				if p.Name != "" {
					out = append(out, Normalize(p.Name))
				}
			}
			return out, nil
		}
		// fall through to HTML attempt if JSON parse fails
	}
	matches := htmlAnchorRE.FindAllStringSubmatch(string(body), -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		name := strings.TrimSpace(stripSlash(m[1]))
		if name == "" {
			continue
		}
		out = append(out, Normalize(name))
	}
	return out, nil
}

// ParseUpstreamProject fetches <upstream>/simple/<normalizedProject>/ and
// returns the per-file entries. Same JSON-then-HTML fallback as
// ParseUpstreamSimpleIndex. Returned URLs are resolved against the request
// URL so callers always have absolute fetch targets.
func ParseUpstreamProject(ctx context.Context, client *http.Client, upstream, normalizedProject string, creds AuthCreds) ([]UpstreamFile, error) {
	if client == nil {
		client = http.DefaultClient
	}
	if normalizedProject == "" {
		return nil, fmt.Errorf("pypi upstream: empty project name")
	}
	projectURL := simpleBaseURL(upstream) + "/simple/" + url.PathEscape(normalizedProject) + "/"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, projectURL, nil)
	if err != nil {
		return nil, fmt.Errorf("pypi upstream: build req: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.pypi.simple.v1+json, text/html;q=0.5")
	upstreamfetch.ApplyCreds(req, creds)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("pypi upstream: get %s: %w", projectURL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("pypi upstream: %s -> %d", projectURL, resp.StatusCode)
	}
	// Fail-explicit on cap+1 instead of the prior silent-truncation idiom
	// (full-buffer read through a LimitReader).
	body, err := streamio.ReadAllLimited(resp.Body, maxProjectPageBytes, streamio.ErrMetadataTooLarge)
	if err != nil {
		return nil, fmt.Errorf("pypi upstream: read body: %w", err)
	}
	base, _ := url.Parse(projectURL)
	if isJSON(resp.Header.Get("Content-Type")) {
		var pj pep691Project
		if jerr := json.Unmarshal(body, &pj); jerr == nil {
			out := make([]UpstreamFile, 0, len(pj.Files))
			for _, f := range pj.Files {
				if f.Filename == "" || f.URL == "" {
					continue
				}
				abs := resolveURL(base, f.URL)
				out = append(out, UpstreamFile{
					Filename:       f.Filename,
					URL:            abs,
					SHA256:         f.Hashes["sha256"],
					RequiresPython: f.RequiresPython,
					Size:           f.Size,
				})
			}
			return out, nil
		}
	}
	// HTML fallback.
	out := make([]UpstreamFile, 0, 16)
	matches := htmlFileRE.FindAllStringSubmatchIndex(string(body), -1)
	for _, m := range matches {
		hrefURL := string(body[m[2]:m[3]])
		sha := string(body[m[4]:m[5]])
		filename := strings.TrimSpace(string(body[m[6]:m[7]]))
		// Look back at the surrounding anchor for data-requires-python.
		anchorStart := m[0]
		anchorEnd := m[1]
		anchor := string(body[anchorStart:anchorEnd])
		var rp string
		if rm := htmlRequiresPythonRE.FindStringSubmatch(anchor); len(rm) >= 2 {
			rp = rm[1]
		}
		abs := resolveURL(base, hrefURL)
		out = append(out, UpstreamFile{
			Filename:       filename,
			URL:            abs,
			SHA256:         strings.ToLower(sha),
			RequiresPython: rp,
		})
	}
	return out, nil
}

func isJSON(ct string) bool {
	ct = strings.ToLower(ct)
	return strings.Contains(ct, "json")
}

func stripSlash(s string) string {
	return strings.Trim(s, "/")
}

func resolveURL(base *url.URL, href string) string {
	if base == nil {
		return href
	}
	rel, err := url.Parse(href)
	if err != nil {
		return href
	}
	return base.ResolveReference(rel).String()
}

// FilterFile reports whether f passes the filter. Empty filter accepts all.
func (sf SyncFilter) FilterFile(f UpstreamFile, normalizedProject string) bool {
	if len(sf.Names) == 0 && len(sf.Globs) == 0 {
		return true
	}
	if len(sf.Names) > 0 {
		for _, n := range sf.Names {
			if Normalize(n) == normalizedProject {
				return true
			}
		}
	}
	if len(sf.Globs) > 0 {
		for _, g := range sf.Globs {
			if ok, _ := filepath.Match(g, f.Filename); ok {
				return true
			}
		}
	}
	return false
}

// AcceptProject reports whether the per-project iteration should run for
// normalizedProject. Used to skip projects entirely before fetching.
func (sf SyncFilter) AcceptProject(normalizedProject string) bool {
	if len(sf.Names) == 0 {
		return true
	}
	for _, n := range sf.Names {
		if Normalize(n) == normalizedProject {
			return true
		}
	}
	return false
}

// isInstallableExt reports whether filename has a suffix pip will actually
// install from a PEP 503 simple index in 2026. Only wheels and the three
// sdist archive extensions matter: pip stopped installing from .egg via
// simple/ in 2017, and .exe / .msi bdists haven't been accepted as new
// uploads on pypi.org for just as long.
//
// The mirror sync must reject non-installable upstream entries up front:
// the inline sync-path version parser in sync_handler.go strips only
// .gz/.tar/.zip, so legacy files whose filename tail contains a dash —
// e.g. `requests-2.23.0-py2.7.egg` — land in pypi_files with the tail as
// the "version" and pollute the Simple-index grouping, scan-result cards,
// and UI collapsed rows.
func isInstallableExt(filename string) bool {
	l := strings.ToLower(filename)
	switch {
	case strings.HasSuffix(l, ".whl"):
		return true
	case strings.HasSuffix(l, ".tar.gz"),
		strings.HasSuffix(l, ".tgz"),
		strings.HasSuffix(l, ".zip"):
		return true
	default:
		return false
	}
}

// maxMirrorFilenameLen caps the accepted filename length below ext4's
// 255-byte NAME_MAX so PathStore.Put fails at the allowlist boundary with
// a clear error rather than late at the syscall with ENAMETOOLONG. No
// real-world PyPI artefact comes close — the longest observable wheels
// on pypi.org sit well under 180 characters.
const maxMirrorFilenameLen = 200

// isSafeMirrorFilename gates upstream-fed filenames before they ever
// become a path segment under {proj}/pypi/{repo}/packages/. The allowlist
// is intentionally narrow: PEP 427 wheels and PEP 625 sdists only use
// letters, digits, dot, underscore, and hyphen. Anything else (slashes,
// control chars, quotes, angle brackets, null bytes) is either a
// directory-separator attack, a header-injection attempt, or a typo
// from a hostile upstream.
//
// The pypi_sync ingest path previously passed upstream filenames through
// to PathStore verbatim. The web side escapes them for display, but a
// malicious mirror could still engineer Content-Disposition-influencing
// bytes if we ever served the file with an attachment header. Rejecting
// at ingest is the cheaper defence.
func isSafeMirrorFilename(name string) bool {
	if name == "" || len(name) > maxMirrorFilenameLen {
		return false
	}
	if name[0] == '.' || name[0] == '-' {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'A' && c <= 'Z':
		case c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9':
		case c == '.' || c == '_' || c == '-':
		default:
			return false
		}
	}
	return true
}
