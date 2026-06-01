// Package helm implements the Helm v3 chart-repository protocol
// (HELM-01..04, Phase 3 Plan 02).
//
// Routes mounted under /<project>/helm/<repo>/:
//   - PUT    charts/<filename>           — upload .tgz chart (parses Chart.yaml)
//   - PUT    charts/<filename>.prov      — pass-through provenance blob
//   - GET    index.yaml                  — serves the regenerated index
//   - GET    charts/<filename>           — serves .tgz / .prov bytes
//   - DELETE charts/<filename>           — moves chart (+ .prov) to trash
//
// Uploads parse Chart.yaml via helm.sh/helm/v3/pkg/chart/loader and insert a
// helm_charts row + helm_fts row in one writer tx; a coalescer.Kick() after
// commit triggers debounced index.yaml regeneration from disk via
// helm.sh/helm/v3/pkg/repo.IndexDirectory (see regen.go).
//
// Private key material never appears here; this package touches only
// public-facing chart metadata.
package helm

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
)

// Chart is the subset of Chart.yaml that the helm_charts row and helm_fts
// index care about. Keywords / Maintainers are JSON-encoded by the caller
// before landing in the DB; the parse step keeps them structured so index.yaml
// regen can consume them directly.
type Chart struct {
	Name        string
	Version     string
	AppVersion  string
	Description string
	Keywords    []string
	Maintainers []chart.Maintainer
}

// Parse opens the .tgz archive at path and returns its Chart metadata. Fails
// with a typed error if the archive is not a valid Helm chart — the handler
// surfaces this as "invalid_package".
func Parse(path string) (*Chart, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("helm: open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	c, err := loader.LoadArchive(f)
	if err != nil {
		return nil, fmt.Errorf("helm: load archive: %w", err)
	}
	if c == nil || c.Metadata == nil {
		return nil, errors.New("helm: missing Chart.yaml metadata")
	}
	if c.Metadata.Name == "" || c.Metadata.Version == "" {
		return nil, errors.New("helm: Chart.yaml missing required name/version")
	}

	out := &Chart{
		Name:        c.Metadata.Name,
		Version:     c.Metadata.Version,
		AppVersion:  c.Metadata.AppVersion,
		Description: c.Metadata.Description,
		Keywords:    append([]string(nil), c.Metadata.Keywords...),
	}
	for _, m := range c.Metadata.Maintainers {
		if m == nil {
			continue
		}
		out.Maintainers = append(out.Maintainers, *m)
	}
	return out, nil
}

// KeywordsJSON returns the keywords slice encoded as a stable JSON array, or
// "[]" when empty — matches helm_charts.keywords_json storage convention.
func (c *Chart) KeywordsJSON() string {
	if len(c.Keywords) == 0 {
		return "[]"
	}
	b, err := json.Marshal(c.Keywords)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// MaintainersJSON returns the maintainers slice encoded as a stable JSON
// array, or "[]" when empty.
func (c *Chart) MaintainersJSON() string {
	if len(c.Maintainers) == 0 {
		return "[]"
	}
	b, err := json.Marshal(c.Maintainers)
	if err != nil {
		return "[]"
	}
	return string(b)
}
