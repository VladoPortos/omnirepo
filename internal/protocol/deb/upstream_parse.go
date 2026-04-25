// Package deb — Phase 03 Plan 06 SYNC-05 upstream parser.
//
// Fetches an upstream APT repo's dists/<suite>/InRelease (or Release) and
// the per-(component, arch) Packages(.gz) files, then yields one
// UpstreamEntry per parsed control paragraph.
package deb

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/dxc-internal/omnirepo/internal/streamio"
)

// maxPackagesDecompressedBytes caps the gunzipped Packages stream.
// Test-overridable (var, not const) so cap+1 oversized-upstream regression
// guards can run without crafting a multi-GiB gzip bomb. Production
// callers do not mutate it.
var maxPackagesDecompressedBytes int64 = 1024 * 1024 * 1024

// UpstreamEntry is one binary package yielded by ParseUpstream.
type UpstreamEntry struct {
	Path      string // absolute URL to fetch the .deb
	Digest    string // "sha256:<hex>" from upstream Packages SHA256: field
	Size      int64
	Filename  string // basename of the pool/ Filename: field
	Suite     string
	Component string
	Arch      string
	Control   *Control
}

// AuthCreds carries optional Basic / Bearer credentials.
type AuthCreds struct {
	User, Password, Token string
}

// SyncFilter narrows the per-entry yield. Names match the package name
// (case-insensitive); Globs match the candidate filename via filepath.Match.
// Suites/Components/Arches narrow the (suite, component, arch) tuples
// expanded from Release; empty = all declared in Release.
type SyncFilter struct {
	Names      []string
	Globs      []string
	Suites     []string
	Components []string
	Arches     []string
}

// ParseUpstream walks the upstream APT structure for one suite (taken from
// upstreamURL, defaulting to "stable" when unspecified) and yields entries.
//
// upstreamURL must be the dist root, e.g. http://upstream.example/debian.
// suite is the dist name (e.g. "stable"); when empty, "stable" is used.
func ParseUpstream(
	ctx context.Context,
	client *http.Client,
	upstreamURL, suite string,
	creds AuthCreds,
	filter SyncFilter,
	yield func(UpstreamEntry) error,
) (int, error) {
	if client == nil {
		client = http.DefaultClient
	}
	if suite == "" {
		suite = "stable"
	}
	if !filter.acceptSuite(suite) {
		return 0, nil
	}
	base, err := url.Parse(strings.TrimRight(upstreamURL, "/") + "/")
	if err != nil {
		return 0, fmt.Errorf("deb upstream: parse url: %w", err)
	}
	// Try InRelease first, fall back to Release.
	releaseURL := base.ResolveReference(&url.URL{Path: "dists/" + suite + "/InRelease"}).String()
	body, err := fetchAll(ctx, client, releaseURL, creds, 8*1024*1024)
	if err != nil {
		releaseURL = base.ResolveReference(&url.URL{Path: "dists/" + suite + "/Release"}).String()
		body, err = fetchAll(ctx, client, releaseURL, creds, 8*1024*1024)
		if err != nil {
			return 0, err
		}
	}
	rel := parseReleaseFile(body)

	components := rel.Components
	if len(components) == 0 {
		components = []string{"main"}
	}
	arches := rel.Architectures
	if len(arches) == 0 {
		arches = []string{"amd64"}
	}

	count := 0
	for _, comp := range components {
		if !filter.acceptComponent(comp) {
			continue
		}
		for _, arch := range arches {
			if !filter.acceptArch(arch) {
				continue
			}
			rel := "dists/" + suite + "/" + comp + "/binary-" + arch + "/Packages.gz"
			pkgsURL := base.ResolveReference(&url.URL{Path: rel}).String()
			pkgsBody, err := fetchAll(ctx, client, pkgsURL, creds, 256*1024*1024)
			if err != nil {
				// Try uncompressed Packages as a fallback.
				rel = "dists/" + suite + "/" + comp + "/binary-" + arch + "/Packages"
				pkgsURL = base.ResolveReference(&url.URL{Path: rel}).String()
				pkgsBody, err = fetchAll(ctx, client, pkgsURL, creds, 1024*1024*1024)
				if err != nil {
					continue
				}
			} else {
				gz, gerr := gzip.NewReader(bytes.NewReader(pkgsBody))
				if gerr != nil {
					return count, fmt.Errorf("deb upstream: gunzip Packages: %w", gerr)
				}
				// STREAMIO-06 (audit #5): fail-explicit on cap+1 instead
				// of the previous silent-truncation idiom on the
				// gunzipped Packages stream. Cap layer is post-decompress
				// so a small compressed body that decompresses to cap+1
				// is rejected.
				expanded, rerr := streamio.ReadAllLimited(gz, maxPackagesDecompressedBytes, streamio.ErrMetadataTooLarge)
				_ = gz.Close()
				if rerr != nil {
					return count, fmt.Errorf("deb upstream: read Packages: %w", rerr)
				}
				pkgsBody = expanded
			}

			paragraphs := splitParagraphs(pkgsBody)
			for _, para := range paragraphs {
				ent, ok, err := parsePackagesParagraph(para, base, suite, comp, arch)
				if err != nil {
					return count, err
				}
				if !ok {
					continue
				}
				if !filter.acceptName(ent.Control.Package) {
					continue
				}
				if !filter.acceptFilename(ent.Filename) {
					continue
				}
				if err := yield(ent); err != nil {
					return count, err
				}
				count++
			}
		}
	}
	return count, nil
}

// release file fields we care about.
type releaseInfo struct {
	Suite         string
	Codename      string
	Components    []string
	Architectures []string
}

// parseReleaseFile extracts Components: and Architectures: from a Release
// or InRelease body. Clearsign wrappers (`-----BEGIN PGP SIGNED MESSAGE-----`)
// are tolerated by skipping the dash-armor lines.
func parseReleaseFile(body []byte) releaseInfo {
	var info releaseInfo
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "-----BEGIN") || strings.HasPrefix(line, "-----END") ||
			strings.HasPrefix(line, "Hash:") || line == "" || strings.HasPrefix(line, " ") {
			continue
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		val = strings.TrimSpace(val)
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "suite":
			info.Suite = val
		case "codename":
			info.Codename = val
		case "components":
			info.Components = strings.Fields(val)
		case "architectures":
			info.Architectures = strings.Fields(val)
		}
	}
	return info
}

func splitParagraphs(body []byte) [][]byte {
	out := [][]byte{}
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 256*1024), 16*1024*1024)
	var cur bytes.Buffer
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if cur.Len() > 0 {
				out = append(out, append([]byte(nil), cur.Bytes()...))
				cur.Reset()
			}
			continue
		}
		cur.WriteString(line)
		cur.WriteByte('\n')
	}
	if cur.Len() > 0 {
		out = append(out, cur.Bytes())
	}
	return out
}

func parsePackagesParagraph(body []byte, base *url.URL, suite, comp, arch string) (UpstreamEntry, bool, error) {
	ctrl, err := ParseControlParagraph(body)
	if err != nil {
		return UpstreamEntry{}, false, nil
	}
	// Extract Filename + Size + SHA256 directly from the paragraph; control
	// struct doesn't carry those Packages-only fields.
	var (
		filename string
		sizeStr  string
		sha256   string
	)
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "Filename:") &&
			!strings.HasPrefix(line, "Size:") &&
			!strings.HasPrefix(line, "SHA256:") {
			continue
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		val = strings.TrimSpace(val)
		switch key {
		case "Filename":
			filename = val
		case "Size":
			sizeStr = val
		case "SHA256":
			sha256 = val
		}
	}
	if filename == "" {
		return UpstreamEntry{}, false, nil
	}
	size, _ := strconv.ParseInt(sizeStr, 10, 64)
	digest := ""
	if sha256 != "" {
		digest = "sha256:" + strings.ToLower(sha256)
	}
	fetchURL := base.ResolveReference(&url.URL{Path: filename}).String()
	return UpstreamEntry{
		Path:      fetchURL,
		Digest:    digest,
		Size:      size,
		Filename:  path.Base(filename),
		Suite:     suite,
		Component: comp,
		Arch:      arch,
		Control:   ctrl,
	}, true, nil
}

func (sf SyncFilter) acceptName(name string) bool {
	if len(sf.Names) == 0 {
		return true
	}
	lower := strings.ToLower(name)
	for _, n := range sf.Names {
		if strings.ToLower(n) == lower {
			return true
		}
	}
	return false
}

func (sf SyncFilter) acceptFilename(filename string) bool {
	if len(sf.Globs) == 0 {
		return true
	}
	for _, g := range sf.Globs {
		if ok, _ := filepath.Match(g, filename); ok {
			return true
		}
	}
	return false
}

func (sf SyncFilter) acceptSuite(s string) bool {
	if len(sf.Suites) == 0 {
		return true
	}
	for _, x := range sf.Suites {
		if x == s {
			return true
		}
	}
	return false
}

func (sf SyncFilter) acceptComponent(c string) bool {
	if len(sf.Components) == 0 {
		return true
	}
	for _, x := range sf.Components {
		if x == c {
			return true
		}
	}
	return false
}

func (sf SyncFilter) acceptArch(a string) bool {
	if len(sf.Arches) == 0 {
		return true
	}
	for _, x := range sf.Arches {
		if x == a {
			return true
		}
	}
	return false
}

func applyCreds(req *http.Request, creds AuthCreds) {
	switch {
	case creds.Token != "":
		req.Header.Set("Authorization", "Bearer "+creds.Token)
	case creds.User != "" || creds.Password != "":
		req.SetBasicAuth(creds.User, creds.Password)
	}
}

func fetchAll(ctx context.Context, client *http.Client, urlStr string, creds AuthCreds, maxBytes int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return nil, fmt.Errorf("deb upstream: build req: %w", err)
	}
	applyCreds(req, creds)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("deb upstream: get %s: %w", urlStr, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("deb upstream: %s -> %d", urlStr, resp.StatusCode)
	}
	// STREAMIO-06 (audit #5): fail-explicit on cap+1 metadata bodies
	// (Release / InRelease / Packages / Packages.gz) instead of the prior
	// silent-truncation idiom.
	body, err := streamio.ReadAllLimited(resp.Body, maxBytes, streamio.ErrMetadataTooLarge)
	if err != nil {
		return nil, fmt.Errorf("deb upstream: read body: %w", err)
	}
	return body, nil
}
