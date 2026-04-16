// Package rpm — Phase 03 Plan 06 SYNC-05 upstream parser.
//
// Fetches an upstream RPM repo's repodata/repomd.xml, locates the primary
// data block, downloads + gunzips primary.xml.gz, and yields one
// UpstreamEntry per <package>. Reuses the Repomd* and Primary* XML structs
// from repodata.go (write-side schema is symmetric with read-side).
package rpm

import (
	"compress/gzip"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
)

// UpstreamEntry is one package yielded by ParseUpstream.
type UpstreamEntry struct {
	Path     string // absolute URL to fetch the .rpm
	Digest   string // "sha256:<hex>" when upstream <checksum type="sha256">
	Size     int64
	Filename string
	Metadata *PrimaryPkg
}

// AuthCreds carries optional Basic / Bearer credentials.
type AuthCreds struct {
	User, Password, Token string
}

// SyncFilter narrows the per-entry yield. Names match the package name
// (case-insensitive); Globs match the candidate filename via filepath.Match.
type SyncFilter struct {
	Names []string
	Globs []string
}

// ParseUpstream fetches the upstream repomd.xml + primary.xml.gz and
// invokes yield once per package.
func ParseUpstream(
	ctx context.Context,
	client *http.Client,
	upstreamURL string,
	creds AuthCreds,
	filter SyncFilter,
	yield func(UpstreamEntry) error,
) (int, error) {
	if client == nil {
		client = http.DefaultClient
	}
	base, err := url.Parse(strings.TrimRight(upstreamURL, "/") + "/")
	if err != nil {
		return 0, fmt.Errorf("rpm upstream: parse url: %w", err)
	}
	repomdURL := base.ResolveReference(&url.URL{Path: "repodata/repomd.xml"}).String()
	repomdBody, err := fetchAll(ctx, client, repomdURL, creds, 8*1024*1024)
	if err != nil {
		return 0, err
	}
	var repomd RepomdRoot
	if err := xml.Unmarshal(repomdBody, &repomd); err != nil {
		return 0, fmt.Errorf("rpm upstream: parse repomd: %w", err)
	}
	var primaryHref string
	for _, d := range repomd.Data {
		if d.Type == "primary" {
			primaryHref = d.Location.Href
			break
		}
	}
	if primaryHref == "" {
		return 0, fmt.Errorf("rpm upstream: no primary block in repomd")
	}
	primaryURL := base.ResolveReference(&url.URL{Path: primaryHref}).String()
	primaryGZ, err := fetchAll(ctx, client, primaryURL, creds, 256*1024*1024)
	if err != nil {
		return 0, err
	}
	gz, err := gzip.NewReader(strings.NewReader(string(primaryGZ)))
	if err != nil {
		return 0, fmt.Errorf("rpm upstream: gunzip primary: %w", err)
	}
	primaryBody, err := io.ReadAll(io.LimitReader(gz, 1024*1024*1024))
	if err != nil {
		_ = gz.Close()
		return 0, fmt.Errorf("rpm upstream: read primary: %w", err)
	}
	// ME-14: a failed Close on a gzip reader indicates the upstream stream
	// was truncated/corrupt — fail the parse rather than importing half of
	// primary.xml silently.
	if err := gz.Close(); err != nil {
		return 0, fmt.Errorf("rpm upstream: close primary gz: %w", err)
	}
	var root PrimaryRoot
	if err := xml.Unmarshal(primaryBody, &root); err != nil {
		return 0, fmt.Errorf("rpm upstream: parse primary: %w", err)
	}

	count := 0
	for i := range root.Pkgs {
		pkg := &root.Pkgs[i]
		if !filter.acceptName(pkg.Name) {
			continue
		}
		fetchURL := base.ResolveReference(&url.URL{Path: pkg.Location.Href}).String()
		filename := filepath.Base(pkg.Location.Href)
		if !filter.acceptFilename(filename) {
			continue
		}
		digest := ""
		if strings.EqualFold(pkg.Checksum.Type, "sha256") && pkg.Checksum.Value != "" {
			digest = "sha256:" + strings.ToLower(pkg.Checksum.Value)
		}
		ent := UpstreamEntry{
			Path:     fetchURL,
			Digest:   digest,
			Size:     pkg.Size.Package,
			Filename: filename,
			Metadata: pkg,
		}
		if err := yield(ent); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
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

func applyCreds(req *http.Request, creds AuthCreds) {
	switch {
	case creds.Token != "":
		req.Header.Set("Authorization", "Bearer "+creds.Token)
	case creds.User != "" || creds.Password != "":
		req.SetBasicAuth(creds.User, creds.Password)
	}
}

// fetchAll GETs urlStr with auth + ctx, reading at most maxBytes from the
// response body. Non-2xx responses return an error referencing the URL but
// without body bytes.
func fetchAll(ctx context.Context, client *http.Client, urlStr string, creds AuthCreds, maxBytes int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return nil, fmt.Errorf("rpm upstream: build req: %w", err)
	}
	applyCreds(req, creds)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("rpm upstream: get %s: %w", urlStr, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("rpm upstream: %s -> %d", urlStr, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes))
	if err != nil {
		return nil, fmt.Errorf("rpm upstream: read body: %w", err)
	}
	return body, nil
}
