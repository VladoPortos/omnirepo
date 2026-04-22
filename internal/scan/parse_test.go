package scan_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
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

// F-08.2: Trivy misconfiguration findings (Helm/IaC scans) must be
// counted in the severity summary — otherwise the repos.block_on_severity
// gate silently passes every Helm chart, no matter how many HIGH/CRITICAL
// misconfigs it has.
func TestParseTrivyMisconfigurationsCountInSummary(t *testing.T) {
	doc := `{
      "SchemaVersion": 2,
      "ArtifactName": "helm-chart",
      "Results": [
        {"Target":"vulny/templates/pod.yaml","Class":"config","Type":"helm",
         "Misconfigurations":[
           {"ID":"KSV-0017","Title":"Privileged container","Description":"container privileged=true","Severity":"HIGH","Resolution":"unset privileged","Status":"FAIL"},
           {"ID":"KSV-0012","Title":"Run as root","Description":"user 0","Severity":"HIGH","Status":"FAIL"},
           {"ID":"KSV-0001","Title":"Can elevate","Description":"escalation","Severity":"MEDIUM","Status":"FAIL"},
           {"ID":"KSV-0099","Title":"resolved already","Severity":"HIGH","Status":"PASS"}
         ]}
      ]
    }`
	res, err := scan.ParseTrivyJSON([]byte(doc))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if res.Summary["high"] != 2 {
		t.Errorf("Summary[high] = %d, want 2 (FAIL status only — PASS must not count)", res.Summary["high"])
	}
	if res.Summary["medium"] != 1 {
		t.Errorf("Summary[medium] = %d, want 1", res.Summary["medium"])
	}
	if len(res.Vulnerabilities) != 3 {
		t.Errorf("Vulnerabilities len = %d, want 3 (3 FAILs folded in)", len(res.Vulnerabilities))
	}
	// Sanity: misconfig IDs land in CVEID and Resolution in Description.
	var sawPriv bool
	for _, v := range res.Vulnerabilities {
		if v.CVEID == "KSV-0017" {
			sawPriv = true
			if !strings.Contains(v.Description, "Resolution: unset privileged") {
				t.Errorf("KSV-0017 Description missing resolution: %q", v.Description)
			}
		}
	}
	if !sawPriv {
		t.Error("KSV-0017 missing from Vulnerabilities slice")
	}
}

// F-08.2 follow-up: vulnerabilities and misconfigurations cohabit in the
// same Results list — make sure both are counted and the totals are
// additive (not overwritten).
func TestParseTrivyVulnsPlusMisconfigsAdditive(t *testing.T) {
	doc := `{
      "SchemaVersion": 2,
      "Results": [
        {"Target":"os-pkgs","Class":"os-pkgs","Vulnerabilities":[
          {"VulnerabilityID":"CVE-2024-0001","PkgName":"openssl","Severity":"CRITICAL","Title":"t","Description":"d"}
        ]},
        {"Target":"helm/pod.yaml","Class":"config","Type":"helm","Misconfigurations":[
          {"ID":"KSV-0017","Title":"priv","Severity":"HIGH","Status":"FAIL"}
        ]}
      ]
    }`
	res, err := scan.ParseTrivyJSON([]byte(doc))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if res.Summary["critical"] != 1 || res.Summary["high"] != 1 {
		t.Errorf("Summary = %+v, want critical=1 high=1", res.Summary)
	}
	if len(res.Vulnerabilities) != 2 {
		t.Errorf("Vulnerabilities len = %d, want 2", len(res.Vulnerabilities))
	}
}
