package helm_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"testing"
)

// makeChartTGZ builds an in-memory minimal Helm chart archive with the
// supplied Chart.yaml fields. The archive layout matches what
// helm.sh/helm/v3/pkg/chart/loader.LoadArchive expects: a single top-level
// directory named after the chart containing at least Chart.yaml. We also
// include a stub templates/NOTES.txt because some loader paths fail softly
// without at least one templates entry; this keeps test fixtures honest.
func makeChartTGZ(t *testing.T, name, version, appVersion, description string, keywords []string) []byte {
	t.Helper()
	var keywordsYAML string
	if len(keywords) > 0 {
		keywordsYAML = "keywords:\n"
		for _, k := range keywords {
			keywordsYAML += fmt.Sprintf("  - %s\n", k)
		}
	}
	chartYAML := fmt.Sprintf(`apiVersion: v2
name: %s
version: %s
appVersion: "%s"
description: %s
type: application
%s`, name, version, appVersion, description, keywordsYAML)

	notes := "Test chart NOTES\n"

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	writeTarFile := func(path, body string) {
		h := &tar.Header{
			Name:     path,
			Mode:     0o644,
			Size:     int64(len(body)),
			Typeflag: tar.TypeReg,
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatalf("tar header %s: %v", path, err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatalf("tar body %s: %v", path, err)
		}
	}
	writeTarFile(name+"/Chart.yaml", chartYAML)
	writeTarFile(name+"/templates/NOTES.txt", notes)
	// values.yaml optional but loader tolerates absence.

	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gz close: %v", err)
	}
	return buf.Bytes()
}
