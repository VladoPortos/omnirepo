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

	"github.com/Masterminds/semver/v3"
	"github.com/dxc-internal/omnirepo/internal/protocol/helm/ociclient"
	helmrepo "helm.sh/helm/v3/pkg/repo"
)

// EntrySourceKind classifies the fetch transport for a single upstream
// index entry. Tagged onto UpstreamEntry so sync_handler.fetchAndCommit
// (plan 11-03) can dispatch to HTTP vs OCI code paths (OCIHELM-03).
//
// ParseUpstream sets Source from the entry's Path prefix:
//   - "http://" or "https://" → EntrySourceHTTP
//   - "oci://"               → EntrySourceOCI
//   - anything else          → EntrySourceUnknown (skipped downstream)
type EntrySourceKind int

const (
	// EntrySourceUnknown is the zero value — ParseUpstream assigns it to
	// any path that lacks a recognized scheme prefix. The v1.2 sync
	// handler's existing "skip unsupported" branch will drop these.
	EntrySourceUnknown EntrySourceKind = iota
	// EntrySourceHTTP is the pre-v1.4 default — chart tgz fetched over
	// http(s) from the URL recorded in the upstream index.yaml.
	EntrySourceHTTP
	// EntrySourceOCI means the chart lives at an oci:// reference and
	// must be pulled via the ociclient subpackage (plan 11-03).
	EntrySourceOCI
)

// String returns the short kind name used in audit/log output.
func (k EntrySourceKind) String() string {
	switch k {
	case EntrySourceHTTP:
		return "http"
	case EntrySourceOCI:
		return "oci"
	default:
		return "unknown"
	}
}

// UpstreamEntry is one chart version yielded by ParseUpstream.
type UpstreamEntry struct {
	Path     string // absolute URL to fetch the .tgz
	Digest   string // "sha256:<hex>" or "" if upstream omitted it
	Size     int64
	Filename string // canonical chart filename (<name>-<version>.tgz)
	Metadata *helmrepo.ChartVersion
	Source   EntrySourceKind // http vs oci vs unknown (plan 11-02)
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
				Source:   classifyEntryPath(fetchURL),
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

// classifyEntryPath returns the EntrySourceKind for a post-resolveURL entry
// path. Case-insensitive on the scheme. Plan 11-02 introduces this tag so
// plan 11-03's fetchAndCommit can dispatch to HTTP vs OCI code paths
// without re-parsing the URL. OCIHELM-03.
func classifyEntryPath(path string) EntrySourceKind {
	lower := strings.ToLower(path)
	switch {
	case strings.HasPrefix(lower, "oci://"):
		return EntrySourceOCI
	case strings.HasPrefix(lower, "http://"), strings.HasPrefix(lower, "https://"):
		return EntrySourceHTTP
	default:
		return EntrySourceUnknown
	}
}

// chartNameFromOCIRef returns the last path segment of an oci:// ref with
// no tag (e.g. "oci://registry-1.docker.io/bitnamicharts/nginx" → "nginx").
// Returns "" for refs that don't parse. Used by ParseOCIUpstream to derive
// the helm chart name for filter matching and filename synthesis.
func chartNameFromOCIRef(ref string) string {
	trimmed := strings.TrimPrefix(strings.ToLower(ref), "oci://")
	// Strip any tag suffix so pure-path refs and tagged refs both work.
	if at := strings.LastIndex(trimmed, ":"); at > strings.LastIndex(trimmed, "/") {
		trimmed = trimmed[:at]
	}
	trimmed = strings.TrimRight(trimmed, "/")
	if idx := strings.LastIndex(trimmed, "/"); idx >= 0 && idx+1 < len(trimmed) {
		return trimmed[idx+1:]
	}
	return ""
}

// ParseOCIUpstream enumerates tags for a pure oci:// helm upstream and
// yields one UpstreamEntry per semver-parseable tag. The top-level URL
// points at a single chart repo (e.g.
// "oci://registry-1.docker.io/bitnamicharts/nginx"); each tag becomes a
// chart version. This is the OCI equivalent of ParseUpstream for helm
// mirrors whose upstream has no HTTP index.yaml.
//
// Semantics:
//   - Tags that don't parse as SemVer are skipped silently. Helm requires
//     SemVer for Chart.yaml:version; non-semver tags can't become chart
//     versions anyway.
//   - filter.Names — matched against the chart name derived from the ref's
//     last path segment. A single-chart OCI upstream with a non-matching
//     allowlist name yields zero entries (mirrors the HTTP behavior of
//     filtering an index.yaml chart entry).
//   - filter.Globs — matched against the synthetic filename
//     "<chart>-<tag>.tgz".
//   - Digest left empty — fetchAndCommitOCI calls Resolve to get the
//     manifest digest and runs the dedup gate there. Pre-resolving here
//     would double the registry round-trips for no benefit.
//
// The caller supplies an OCIClient; pass nil only for tests that stub out
// tag enumeration upstream. A nil client returns a descriptive error so
// the sync handler's fail() path records it in the audit log.
func ParseOCIUpstream(
	ctx context.Context,
	client ociclient.Client,
	upstreamURL string,
	creds AuthCreds,
	filter SyncFilter,
	yield func(UpstreamEntry) error,
) (int, error) {
	if client == nil {
		return 0, fmt.Errorf("helm oci upstream: OCIClient not wired")
	}
	chart := chartNameFromOCIRef(upstreamURL)
	if chart == "" {
		return 0, fmt.Errorf("helm oci upstream: cannot derive chart name from %q", upstreamURL)
	}
	if !filter.acceptName(chart) {
		return 0, nil
	}
	ociCreds := ociclient.AuthCreds{User: creds.User, Password: creds.Password}
	tags, err := client.ListTags(ctx, upstreamURL, ociCreds)
	if err != nil {
		return 0, fmt.Errorf("helm oci upstream: list tags %s: %w", upstreamURL, err)
	}
	count := 0
	base := strings.TrimRight(strings.TrimSuffix(upstreamURL, "/"), ":")
	for _, tag := range tags {
		if _, verr := semver.NewVersion(tag); verr != nil {
			continue
		}
		filename := fmt.Sprintf("%s-%s.tgz", chart, tag)
		if !filter.acceptFilename(filename) {
			continue
		}
		ent := UpstreamEntry{
			Path:     base + ":" + tag,
			Filename: filename,
			Source:   EntrySourceOCI,
		}
		if err := yield(ent); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}
