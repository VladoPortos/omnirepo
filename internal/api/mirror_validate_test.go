package api

import (
	"encoding/json"
	"strings"
	"testing"
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
		if got := validateMirrorUpstreamURL(tc.in); got != tc.ok {
			t.Errorf("validateMirrorUpstreamURL(%q) = %v, want %v", tc.in, got, tc.ok)
		}
	}
}
