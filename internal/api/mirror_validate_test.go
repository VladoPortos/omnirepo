package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/dxc-internal/omnirepo/internal/httperr"
)

func TestValidateMirrorFilter_EmptyIsLegal(t *testing.T) {
	for _, in := range []json.RawMessage{nil, {}} {
		ok, out := validateMirrorFilter("deb", in)
		if !ok || out != nil {
			t.Fatalf("empty filter must round-trip as (true, nil), got (%v, %q)", ok, string(out))
		}
	}
}

func TestValidateMirrorFilter_UnsupportedType(t *testing.T) {
	ok, _ := validateMirrorFilter("docker", json.RawMessage(`{"Names":["x"]}`))
	if ok {
		t.Fatal("docker is not a mirror-supported type; must reject")
	}
}

func TestValidateMirrorFilter_RejectsDuplicateKeys(t *testing.T) {
	// encoding/json silently keeps the last value on duplicate keys. Our
	// token-walker must catch this BEFORE Decode runs, otherwise two callers
	// posting the same filter with different key orderings would see
	// divergent behavior.
	cases := []string{
		`{"Names":["a"],"Names":["b"]}`,
		`{"Names":["a"],"Globs":["*"],"Names":["b"]}`,
	}
	for _, raw := range cases {
		if ok, _ := validateMirrorFilter("deb", json.RawMessage(raw)); ok {
			t.Errorf("duplicate-key payload must be rejected: %s", raw)
		}
	}
}

func TestValidateMirrorFilter_AllowsNestedObjectsWithDisjointKeys(t *testing.T) {
	// The SyncFilter shapes don't actually allow nested objects, so Decode
	// will reject this anyway — but the duplicate-key walker MUST NOT false-
	// positive on identical keys at different depths. Verify via a shape
	// with disjoint top-level keys to keep the DisallowUnknownFields gate
	// happy.
	raw := json.RawMessage(`{"Names":["a"],"Globs":["b"]}`)
	ok, out := validateMirrorFilter("rpm", raw)
	if !ok {
		t.Fatalf("expected valid filter, got rejection")
	}
	if !strings.Contains(string(out), `"Names":["a"]`) || !strings.Contains(string(out), `"Globs":["b"]`) {
		t.Fatalf("canonical form missing expected fields: %s", out)
	}
}

func TestValidateMirrorFilter_RejectsOversizedRawJSON(t *testing.T) {
	// Raw bytes over maxFilterJSONBytes are rejected before any parse, so a
	// grossly oversized payload can't even slip into the decoder.
	big := make([]byte, maxFilterJSONBytes+1)
	for i := range big {
		big[i] = 'a'
	}
	if ok, _ := validateMirrorFilter("deb", big); ok {
		t.Fatal("raw filter exceeding maxFilterJSONBytes must be rejected")
	}
}

func TestValidateMirrorFilter_RejectsTooManyArrayEntries(t *testing.T) {
	names := make([]string, maxFilterArrayEntries+1)
	for i := range names {
		names[i] = "p"
	}
	body, err := json.Marshal(rpmFilterShape{Names: names})
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := validateMirrorFilter("rpm", body); ok {
		t.Fatalf("array with %d entries (cap %d) must be rejected", len(names), maxFilterArrayEntries)
	}
}

func TestValidateMirrorFilter_RejectsOversizedString(t *testing.T) {
	big := strings.Repeat("a", maxFilterStringLen+1)
	body, err := json.Marshal(rpmFilterShape{Names: []string{big}})
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := validateMirrorFilter("rpm", body); ok {
		t.Fatalf("string of length %d (cap %d) must be rejected", len(big), maxFilterStringLen)
	}
}

func TestValidateMirrorFilter_RejectsInvalidUTF8(t *testing.T) {
	// \xff is not valid UTF-8; handcraft the JSON to embed it since
	// json.Marshal would escape arbitrary bytes through \u-escapes.
	raw := json.RawMessage("{\"Names\":[\"\xff\"]}")
	if ok, _ := validateMirrorFilter("rpm", raw); ok {
		t.Fatal("non-UTF-8 string in filter must be rejected")
	}
}

func TestValidateMirrorFilter_NFCNormalizesStrings(t *testing.T) {
	// "café" in NFD: c + a + f + e + U+0301 (combining acute).
	// In NFC: c + a + f + U+00E9.
	nfd := "cafe\u0301"
	nfc := "caf\u00e9"
	raw, err := json.Marshal(rpmFilterShape{Names: []string{nfd}})
	if err != nil {
		t.Fatal(err)
	}
	ok, canonical := validateMirrorFilter("rpm", raw)
	if !ok {
		t.Fatal("legitimate filter with NFD string must be accepted (normalized to NFC)")
	}
	// The canonical form must contain the NFC form, not the NFD form.
	var out rpmFilterShape
	if err := json.Unmarshal(canonical, &out); err != nil {
		t.Fatalf("canonical form must parse: %v", err)
	}
	if len(out.Names) != 1 || out.Names[0] != nfc {
		t.Fatalf("expected NFC form %q, got %q", nfc, out.Names[0])
	}
}

func TestValidateMirrorFilter_RejectsUnknownKeys(t *testing.T) {
	// Regression for the existing DisallowUnknownFields behavior — keep this
	// so the duplicate-key/normalization rework doesn't accidentally relax
	// the schema guard.
	raw := json.RawMessage(`{"NotARealField":["x"]}`)
	if ok, _ := validateMirrorFilter("deb", raw); ok {
		t.Fatal("unknown keys must be rejected by DisallowUnknownFields")
	}
}

func TestValidateMirrorFilter_RejectsMalformedJSON(t *testing.T) {
	raw := json.RawMessage(`{"Names":[`) // truncated
	if ok, _ := validateMirrorFilter("deb", raw); ok {
		t.Fatal("malformed JSON must be rejected")
	}
}

func TestValidateMirrorFilter_CanonicalFormIsStable(t *testing.T) {
	// Two logically-equivalent inputs (different whitespace, same content)
	// must round-trip to the same canonical bytes.
	a := json.RawMessage(`{"Names":["a","b"],"Globs":["*"]}`)
	b := json.RawMessage("{\n  \"Names\":  [\"a\", \"b\"],\n  \"Globs\":[\"*\"]\n}")
	okA, outA := validateMirrorFilter("rpm", a)
	okB, outB := validateMirrorFilter("rpm", b)
	if !okA || !okB {
		t.Fatalf("both inputs must validate: okA=%v okB=%v", okA, okB)
	}
	if string(outA) != string(outB) {
		t.Fatalf("canonical forms diverge:\n  a: %s\n  b: %s", outA, outB)
	}
}

func TestValidateMirrorUpstreamURL(t *testing.T) {
	// Spot-check we didn't regress the URL validator alongside the filter
	// rework. This is a regression-harness only — the primary URL tests live
	// wherever CreateRepo's happy path is exercised.
	//
	// Plan 11-02 widened the signature to (raw, repoType) so the oci://
	// scheme is accepted only for helm mirrors. "deb" is used here to keep
	// these non-helm cases asserting the pre-11-02 behavior unchanged.
	cases := []struct {
		in string
		ok bool
	}{
		{"https://archive.ubuntu.com/ubuntu", true},
		{"http://repo.example/deb", true},
		{"file:///etc/passwd", false},
		{"ftp://legacy.example/pub", false},
		{"javascript:alert(1)", false},
		{"", false},
		{"not a url", false},
		{"https:///no-host", false},
	}
	for _, tc := range cases {
		if got := validateMirrorUpstreamURL(tc.in, "deb"); got != tc.ok {
			t.Errorf("validateMirrorUpstreamURL(%q, \"deb\") = %v, want %v", tc.in, got, tc.ok)
		}
	}
}

// TestClassifyHelmUpstream covers the 11-02 classifier that tags a helm
// mirror upstream URL as http vs oci (or unsupported). Only http/https/oci
// schemes are accepted; bare-host strings without a scheme are rejected in
// this phase (Helm SDK accepts them, but our validator path requires an
// explicit scheme for UX clarity + threat-model simplicity).
func TestClassifyHelmUpstream(t *testing.T) {
	cases := []struct {
		raw     string
		want    HelmSourceKind
		wantErr bool
	}{
		{"http://ex.com/charts", HelmSourceHTTP, false},
		{"https://ex.com/charts", HelmSourceHTTP, false},
		{"HTTPS://EX.COM/charts", HelmSourceHTTP, false}, // scheme is case-insensitive
		{"oci://registry-1.docker.io/bitnamicharts/nginx", HelmSourceOCI, false},
		{"OCI://reg.io/chart", HelmSourceOCI, false},
		{"oci://", HelmSourceUnknown, true},     // missing host+path
		{"oci://host", HelmSourceUnknown, true}, // missing /path
		{"ftp://x", HelmSourceUnknown, true},
		{"", HelmSourceUnknown, true},
		{"file:///etc/passwd", HelmSourceUnknown, true},
		{"javascript:alert(1)", HelmSourceUnknown, true},
		{"not a url", HelmSourceUnknown, true},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			got, err := classifyHelmUpstream(tc.raw)
			if tc.wantErr && err == nil {
				t.Fatalf("classifyHelmUpstream(%q): want error, got nil (kind=%v)", tc.raw, got)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("classifyHelmUpstream(%q): unexpected err: %v", tc.raw, err)
			}
			if got != tc.want {
				t.Fatalf("classifyHelmUpstream(%q) kind: got %v want %v", tc.raw, got, tc.want)
			}
		})
	}
}

// TestValidateMirrorUpstreamURL_HelmAcceptsOCI verifies the 11-02 widening:
// helm mirrors accept oci:// in addition to http(s); other repo types
// continue to reject oci://. Invariant INV-11-02-02: file://, javascript:,
// ftp:// are rejected for all repo types.
func TestValidateMirrorUpstreamURL_HelmAcceptsOCI(t *testing.T) {
	if !validateMirrorUpstreamURL("oci://reg.io/charts/nginx", "helm") {
		t.Fatal("helm should accept oci://reg.io/charts/nginx")
	}
	if validateMirrorUpstreamURL("oci://reg.io/charts/nginx", "rpm") {
		t.Fatal("rpm must NOT accept oci://reg.io/charts/nginx")
	}
	if validateMirrorUpstreamURL("oci://reg.io/charts/nginx", "deb") {
		t.Fatal("deb must NOT accept oci://reg.io/charts/nginx")
	}
	if validateMirrorUpstreamURL("oci://reg.io/charts/nginx", "pypi") {
		t.Fatal("pypi must NOT accept oci://reg.io/charts/nginx")
	}
	if !validateMirrorUpstreamURL("https://ex.com/charts", "helm") {
		t.Fatal("https should still be accepted for helm")
	}
	// Invariant: bad schemes rejected regardless of repo type.
	for _, bad := range []string{"file:///etc/passwd", "javascript:alert(1)", "ftp://x"} {
		for _, rt := range []string{"helm", "deb", "rpm", "pypi"} {
			if validateMirrorUpstreamURL(bad, rt) {
				t.Errorf("validateMirrorUpstreamURL(%q, %q) must be false", bad, rt)
			}
		}
	}
	// Malformed oci:// (missing path) rejected even for helm.
	if validateMirrorUpstreamURL("oci://host", "helm") {
		t.Fatal("oci://host with no path must be rejected")
	}
}

// --- D-3: snake_case mirror filter keys ---

func TestValidateMirrorFilter_AcceptsSnakeCase(t *testing.T) {
	// Every other OmniRepo REST surface uses snake_case; the filter shape was
	// the only PascalCase outlier (matched the Go struct's default JSON
	// encoding). D-3 widens decoding to accept snake_case keys too while
	// keeping PascalCase as the canonical *stored* form.
	cases := []struct {
		repoType string
		raw      string
	}{
		{"deb", `{"names":["bash"],"globs":["lib*"],"suites":["jammy"],"components":["main"],"arches":["amd64"]}`},
		{"rpm", `{"names":["systemd"],"globs":["lib*"]}`},
		{"pypi", `{"names":["requests"]}`},
		{"helm", `{"globs":["nginx-*"]}`},
	}
	for _, tc := range cases {
		t.Run(tc.repoType, func(t *testing.T) {
			ok, out := validateMirrorFilter(tc.repoType, json.RawMessage(tc.raw))
			if !ok {
				t.Fatalf("snake_case filter must validate, got rejection. raw=%s", tc.raw)
			}
			// Stored canonical form stays PascalCase (Go struct default).
			s := string(out)
			if !strings.Contains(s, `"Names"`) && !strings.Contains(s, `"Globs"`) &&
				!strings.Contains(s, `"Suites"`) && !strings.Contains(s, `"Components"`) &&
				!strings.Contains(s, `"Arches"`) {
				t.Fatalf("canonical re-encode lost all PascalCase keys: %s", s)
			}
		})
	}
}

func TestValidateMirrorFilter_AcceptsPascalCaseUnchanged(t *testing.T) {
	// Back-compat: previously-stored PascalCase payloads continue to validate.
	raw := json.RawMessage(`{"Names":["bash"],"Globs":["lib*"]}`)
	ok, out := validateMirrorFilter("rpm", raw)
	if !ok {
		t.Fatalf("legacy PascalCase filter must still validate")
	}
	if !strings.Contains(string(out), `"Names":["bash"]`) {
		t.Fatalf("PascalCase round-trip lost Names: %s", out)
	}
}

func TestValidateMirrorFilter_RejectsMixedCasingForSameField(t *testing.T) {
	// Sending both "Names" and "names" is ambiguous — refuse rather than
	// silently picking one and dropping the other.
	cases := []string{
		`{"Names":["a"],"names":["b"]}`,
		`{"Globs":["x"],"globs":["y"]}`,
	}
	for _, raw := range cases {
		if ok, _ := validateMirrorFilter("rpm", json.RawMessage(raw)); ok {
			t.Errorf("mixed-casing payload must be rejected: %s", raw)
		}
	}
}

// TestMirrorValidate_GitAccepted — plan 11-05 (GITMIRROR-01). Widening the
// mirrorSupportedTypes map lets type=git take the is_mirror=true branch.
// Validator must also accept typical https:// Git remotes via the shared
// URL check.
func TestMirrorValidate_GitAccepted(t *testing.T) {
	if _, ok := mirrorSupportedTypes["git"]; !ok {
		t.Fatalf("mirrorSupportedTypes must include \"git\" after plan 11-05")
	}
	if !validateMirrorUpstreamURL("https://github.com/foo/bar.git", "git") {
		t.Fatal("git mirror must accept https://github.com/foo/bar.git")
	}
}

// TestMirrorValidate_GitRejectsOCI — git is HTTPS-PAT only per GITMIRROR-05.
// oci:// is reserved for helm mirrors (plan 11-02) and must not leak into
// the git branch.
func TestMirrorValidate_GitRejectsOCI(t *testing.T) {
	if validateMirrorUpstreamURL("oci://reg/foo/chart", "git") {
		t.Fatal("git must NOT accept oci:// (GITMIRROR-05 HTTPS-PAT only)")
	}
}

// TestMirrorValidate_GitHTTPOnly — plaintext http is permitted alongside
// https in v1.4 (SSH is the deferred feature, not plaintext-http). Air-gap
// operators running an internal mirror over plain HTTP inside the corporate
// LAN are still supported.
func TestMirrorValidate_GitHTTPOnly(t *testing.T) {
	if !validateMirrorUpstreamURL("http://insecure.example/foo.git", "git") {
		t.Fatal("git mirror must accept http:// in v1.4")
	}
}

// TestMirrorValidate_GitRejectsInvalidSchemes — ssh://, file://, git://,
// javascript: all deferred beyond v1.4. The URL validator's scheme check
// filters them out at the binary accept/reject boundary.
func TestMirrorValidate_GitRejectsInvalidSchemes(t *testing.T) {
	bad := []string{
		"ssh://foo@bar/baz.git",
		"git://github.com/foo/bar.git",
		"file:///etc/passwd",
		"javascript:alert(1)",
		"ftp://legacy/foo.git",
	}
	for _, raw := range bad {
		if validateMirrorUpstreamURL(raw, "git") {
			t.Errorf("git must reject %q (deferred to v1.5+ or unsupported)", raw)
		}
	}
}

// TestMirrorValidate_ExistingProtocolsUnchanged guards against regression
// in the widening — deb/rpm/pypi/helm acceptance semantics stay identical.
func TestMirrorValidate_ExistingProtocolsUnchanged(t *testing.T) {
	for _, rt := range []string{"deb", "rpm", "pypi", "helm"} {
		if _, ok := mirrorSupportedTypes[rt]; !ok {
			t.Errorf("mirrorSupportedTypes must still include %q", rt)
		}
		if !validateMirrorUpstreamURL("https://example.com/repo", rt) {
			t.Errorf("%s must still accept https://", rt)
		}
		if validateMirrorUpstreamURL("file:///etc/passwd", rt) {
			t.Errorf("%s must still reject file://", rt)
		}
	}
	// helm-specific: still accepts oci://
	if !validateMirrorUpstreamURL("oci://reg.io/charts/nginx", "helm") {
		t.Fatal("helm must still accept oci:// after plan 11-05 widening")
	}
	// rpm/deb/pypi still reject oci://
	for _, rt := range []string{"deb", "rpm", "pypi"} {
		if validateMirrorUpstreamURL("oci://reg.io/charts/nginx", rt) {
			t.Errorf("%s must still reject oci://", rt)
		}
	}
}

// TestRefuseDockerHubWithoutCred covers the 11-02 Docker Hub gate (D-04,
// OCIHELM-05). registry-1.docker.io OCI upstreams MUST carry a basic
// credential or the validator returns a 422 httperr.Error with envelope
// code `mirror.docker_hub_requires_credential`. Non-Docker-Hub OCI hosts
// and HTTP upstreams are unaffected.
//
// INV-11-02-01: the envelope code is STABLE across plans — tests assert
// the literal string.
func TestRefuseDockerHubWithoutCred(t *testing.T) {
	cases := []struct {
		name     string
		url      string
		credKind string
		wantCode string // "" = expect nil return (gate not triggered)
	}{
		{"docker_hub_no_cred_oci_scheme", "oci://registry-1.docker.io/bitnamicharts/nginx", "", "mirror.docker_hub_requires_credential"},
		{"docker_hub_with_basic_unblocks", "oci://registry-1.docker.io/bitnamicharts/nginx", "basic", ""},
		{"case_insensitive_host", "oci://Registry-1.Docker.IO/foo", "", "mirror.docker_hub_requires_credential"},
		{"ghcr_no_cred_allowed", "oci://ghcr.io/foo/bar", "", ""},
		{"quay_no_cred_allowed", "oci://quay.io/team/chart", "", ""},
		{"http_upstream_no_cred_allowed", "https://charts.example.com/", "", ""},
		{"http_docker_hub_host_not_gated", "https://registry-1.docker.io/foo", "", ""},
		{"empty_url_not_gated", "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := refuseDockerHubWithoutCred(tc.url, tc.credKind)
			if tc.wantCode == "" {
				if got != nil {
					t.Fatalf("refuseDockerHubWithoutCred(%q,%q): want nil, got envelope %+v (status=%d)", tc.url, tc.credKind, got.Envelope, got.Status)
				}
				return
			}
			if got == nil {
				t.Fatalf("refuseDockerHubWithoutCred(%q,%q): want envelope %q, got nil", tc.url, tc.credKind, tc.wantCode)
			}
			if got.Envelope.Code != tc.wantCode {
				t.Fatalf("envelope.code: got %q want %q", got.Envelope.Code, tc.wantCode)
			}
			if got.Status != http.StatusUnprocessableEntity {
				t.Fatalf("status: got %d want 422", got.Status)
			}
			if got.Envelope.Class != httperr.ClassValidation {
				t.Fatalf("class: got %q want %q", got.Envelope.Class, httperr.ClassValidation)
			}
			// Message (D-04 verbatim copy) must mention the 100/6h rate
			// limit to distinguish it from generic "missing credential"
			// copy — the exact phrase is the operator's only cue.
			if !strings.Contains(got.Envelope.Message, "100 requests") {
				t.Errorf("message missing rate-limit phrase: %q", got.Envelope.Message)
			}
			if !strings.Contains(got.Envelope.Message, "basic credential") {
				t.Errorf("message missing remediation phrase: %q", got.Envelope.Message)
			}
		})
	}
}
