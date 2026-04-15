package scan_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/dxc-internal/omnirepo/internal/scan"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "trivy", name))
	if err != nil {
		t.Fatal(err)
	}
	// sanity: every fixture must itself be valid JSON independent of our parser.
	var any map[string]any
	if err := json.Unmarshal(b, &any); err != nil {
		t.Fatalf("fixture %s is not valid JSON: %v", name, err)
	}
	return b
}

func TestParseTrivyEmptyReturnsErr(t *testing.T) {
	_, err := scan.ParseTrivyJSON(nil)
	if !errors.Is(err, scan.ErrEmptyInput) {
		t.Fatalf("nil input: err = %v, want ErrEmptyInput", err)
	}
	_, err = scan.ParseTrivyJSON([]byte(""))
	if !errors.Is(err, scan.ErrEmptyInput) {
		t.Fatalf("empty input: err = %v, want ErrEmptyInput", err)
	}
}

func TestParseTrivyMalformedJSONWraps(t *testing.T) {
	_, err := scan.ParseTrivyJSON([]byte("{not-json"))
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if errors.Is(err, scan.ErrEmptyInput) {
		t.Errorf("malformed input must NOT look like empty-input: %v", err)
	}
}

func TestParseTrivyNginxCounts(t *testing.T) {
	b := readFixture(t, "v0.69-image-nginx.json")
	res, err := scan.ParseTrivyJSON(b)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := map[string]int{"critical": 5, "high": 12, "medium": 7, "low": 0, "unknown": 0}
	for k, v := range want {
		if got := res.Summary[k]; got != v {
			t.Errorf("Summary[%q] = %d, want %d", k, got, v)
		}
	}
	if len(res.Vulnerabilities) != 5+12+7 {
		t.Errorf("len(Vulnerabilities) = %d, want 24", len(res.Vulnerabilities))
	}
	if res.SchemaVersion != 2 {
		t.Errorf("SchemaVersion = %d, want 2", res.SchemaVersion)
	}
	if res.ArtifactName != "nginx:1.14" {
		t.Errorf("ArtifactName = %q, want nginx:1.14", res.ArtifactName)
	}
}

func TestParseTrivyAlpineToleratesUnknownFields(t *testing.T) {
	b := readFixture(t, "v0.68-image-alpine.json")
	res, err := scan.ParseTrivyJSON(b)
	if err != nil {
		t.Fatalf("parse (alpine fixture with FutureField): %v", err)
	}
	if res.Summary["critical"] != 1 {
		t.Errorf("Summary[critical] = %d, want 1", res.Summary["critical"])
	}
	// All 5 keys must always be present (even at 0).
	for _, k := range []string{"critical", "high", "medium", "low", "unknown"} {
		if _, ok := res.Summary[k]; !ok {
			t.Errorf("Summary missing key %q", k)
		}
	}
}

func TestParseTrivyEmptyFsAllZero(t *testing.T) {
	b := readFixture(t, "v0.67-fs-empty.json")
	res, err := scan.ParseTrivyJSON(b)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	for _, k := range []string{"critical", "high", "medium", "low", "unknown"} {
		if res.Summary[k] != 0 {
			t.Errorf("Summary[%q] = %d, want 0", k, res.Summary[k])
		}
	}
	if len(res.Vulnerabilities) != 0 {
		t.Errorf("len(Vulnerabilities) = %d, want 0", len(res.Vulnerabilities))
	}
}

func TestParseTrivyUnknownSeverityRoutesToUnknownBucket(t *testing.T) {
	doc := `{
      "SchemaVersion": 2,
      "ArtifactName": "synthetic",
      "Results": [
        {"Target":"t","Class":"os-pkgs","Vulnerabilities":[
          {"VulnerabilityID":"CVE-9999-0001","PkgName":"x","Severity":"BOGUS_LEVEL","Title":"t","Description":"d"},
          {"VulnerabilityID":"CVE-9999-0002","PkgName":"y","Severity":"","Title":"t","Description":"d"}
        ]}
      ]
    }`
	res, err := scan.ParseTrivyJSON([]byte(doc))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if res.Summary["unknown"] != 2 {
		t.Errorf("Summary[unknown] = %d, want 2 (BOGUS + empty)", res.Summary["unknown"])
	}
	if len(res.Vulnerabilities) != 2 {
		t.Errorf("Vulnerabilities len = %d, want 2 (neither silently dropped)", len(res.Vulnerabilities))
	}
}
