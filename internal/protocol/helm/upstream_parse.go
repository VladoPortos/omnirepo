// Package helm — Phase 03 Plan 06 SYNC-05 upstream parser.
//
// Fetches <upstream>/index.yaml, parses it via helm.sh/helm/v3/pkg/repo
// LoadIndexFile, and yields one UpstreamEntry per chart version. Chart
// digests come from the Helm index `digest:` field — a sha256 hex string
// the sync handler uses for idempotency without a HEAD request.
package helm

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	helmrepo "helm.sh/helm/v3/pkg/repo"
)

// UpstreamEntry is one chart version yielded by ParseUpstream.
type UpstreamEntry struct {
	Path     string // absolute URL to fetch the .tgz
	Digest   string // "sha256:<hex>" or "" if upstream omitted it
	Size     int64
	Filename string // canonical chart filename (<name>-<version>.tgz)
	Metadata *helmrepo.ChartVersion
}

// AuthCreds carries optional Basic / Bearer credentials.
type AuthCreds struct {
	User, Password, Token string
}

// SyncFilter narrows the per-entry yield. Names match the chart name
// (case-insensitive); Globs match the candidate filename via filepath.Match.
type SyncFilter struct {
	Names []string
	Globs []string
}

// ParseUpstream fetches the upstream index.yaml and invokes yield for each
// chart version after applying filter. Returns the count of yielded
// entries; yield errors short-circuit and are returned to the caller.
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
	indexURL := strings.TrimRight(upstreamURL, "/") + "/index.yaml"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, indexURL, nil)
	if err != nil {
		return 0, fmt.Errorf("helm upstream: build req: %w", err)
	}
	applyCreds(req, creds)
	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("helm upstream: get %s: %w", indexURL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("helm upstream: %s -> %d", indexURL, resp.StatusCode)
	}

	tmp, err := os.CreateTemp("", "helm-upstream-*.yaml")
	if err != nil {
		return 0, fmt.Errorf("helm upstream: tmp: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	const maxIndexBytes = 64 * 1024 * 1024
	if _, err := io.Copy(tmp, io.LimitReader(resp.Body, maxIndexBytes)); err != nil {
		_ = tmp.Close()
		return 0, fmt.Errorf("helm upstream: copy: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return 0, fmt.Errorf("helm upstream: close: %w", err)
	}

	idx, err := helmrepo.LoadIndexFile(tmpPath)
	if err != nil {
		return 0, fmt.Errorf("helm upstream: parse index.yaml: %w", err)
	}

	base, _ := url.Parse(strings.TrimRight(upstreamURL, "/") + "/")
	count := 0
	for chartName, versions := range idx.Entries {
		if !filter.acceptName(chartName) {
			continue
		}
		for _, v := range versions {
			if v == nil {
				continue
			}
			if len(v.URLs) == 0 {
				continue
			}
			fetchURL := resolveURL(base, v.URLs[0])
			filename := filepath.Base(fetchURL)
			if !filter.acceptFilename(filename) {
				continue
			}
			digest := ""
			if v.Digest != "" {
				digest = "sha256:" + strings.ToLower(strings.TrimPrefix(v.Digest, "sha256:"))
			}
			ent := UpstreamEntry{
				Path:     fetchURL,
				Digest:   digest,
				Filename: filename,
				Metadata: v,
			}
			if err := yield(ent); err != nil {
				return count, err
			}
			count++
		}
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
