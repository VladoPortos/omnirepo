package helm_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dxc-internal/omnirepo/internal/protocol/helm"
)

func TestParseMinimalChart(t *testing.T) {
	dir := t.TempDir()
	tgz := makeChartTGZ(t, "mychart", "1.2.3", "v1", "a chart", []string{"foo", "bar"})
	p := filepath.Join(dir, "mychart-1.2.3.tgz")
	if err := os.WriteFile(p, tgz, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	c, err := helm.Parse(p)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if c.Name != "mychart" || c.Version != "1.2.3" {
		t.Fatalf("parsed: %+v", c)
	}
	if c.AppVersion != "v1" {
		t.Fatalf("appVersion: %q", c.AppVersion)
	}
	if c.Description != "a chart" {
		t.Fatalf("description: %q", c.Description)
	}
	if len(c.Keywords) != 2 {
		t.Fatalf("keywords: %v", c.Keywords)
	}
	// JSON helpers produce valid, non-empty JSON arrays.
	if c.KeywordsJSON() == "" || c.KeywordsJSON() == "[]" {
		t.Fatalf("keywords_json: %q", c.KeywordsJSON())
	}
}

func TestParseRejectsNonChartArchive(t *testing.T) {
	dir := t.TempDir()
	// Random bytes — definitely not a valid tgz.
	p := filepath.Join(dir, "bogus.tgz")
	if err := os.WriteFile(p, []byte("not a gzip file at all"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := helm.Parse(p)
	if err == nil {
		t.Fatalf("Parse should have failed")
	}
	if !strings.Contains(err.Error(), "helm:") {
		t.Fatalf("error should be namespaced: %v", err)
	}
}

func TestParseRejectsMissingChartYAML(t *testing.T) {
	// Valid gzip + tar but no Chart.yaml inside.
	dir := t.TempDir()
	tgz := makeChartTGZ(t, "", "", "", "", nil) // empty name → Chart.yaml will lack required fields
	p := filepath.Join(dir, "empty.tgz")
	if err := os.WriteFile(p, tgz, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := helm.Parse(p)
	if err == nil {
		t.Fatalf("Parse should have rejected empty chart")
	}
}

func TestParseEmptyHelpers(t *testing.T) {
	c := &helm.Chart{}
	if got := c.KeywordsJSON(); got != "[]" {
		t.Fatalf("empty keywords: %q", got)
	}
	if got := c.MaintainersJSON(); got != "[]" {
		t.Fatalf("empty maintainers: %q", got)
	}
}
