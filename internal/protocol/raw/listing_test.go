package raw

import (
	"strings"
	"testing"
)

func TestValidateRawPath(t *testing.T) {
	// Build the long inputs once so the slice literal stays readable.
	longSeg := strings.Repeat("a", maxRawPathSegmentBytes+1)
	longSegEdge := strings.Repeat("a", maxRawPathSegmentBytes)
	// Total path just over the 1024-byte cap: four 256-byte segments joined
	// by "/" is 256*4 + 3 = 1027 — also forces the per-segment guard (256 >
	// 255) so we split the case below into a distinct "total length" test.
	totalOverflow := strings.Join([]string{longSegEdge, longSegEdge, longSegEdge, longSegEdge, longSegEdge[:2]}, "/")

	// strict flag maps to the write-path (PUT). strictOnly=true means the
	// case should be rejected on writes but allowed on reads — used for the
	// percent-encoded traversal backward-compat split.
	cases := []struct {
		name       string
		in         string
		want       string
		wantErr    bool
		strictOnly bool
	}{
		{"flat file", "foo.txt", "foo.txt", false, false},
		{"nested file", "a/b/c.txt", "a/b/c.txt", false, false},
		{"leading slash", "/leading/slash", "leading/slash", false, false},
		{"trailing slash", "trailing/", "", true, false}, // empty trailing segment
		{"empty", "", "", true, false},
		{"dot-dot", "..", "", true, false},
		{"mid traversal", "a/../b", "", true, false},
		{"leading dot", "./a", "", true, false},
		{"mid dot", "a/./b", "", true, false},
		{"double slash", "a//b", "", true, false}, // empty middle segment
		{"nul byte", "a/\x00/b", "", true, false}, // NUL byte
		{"multi-level traversal", "a/b/../../c", "", true, false},
		// percent-encoded traversal must not slip through writes,
		// but reads still resolve them (legacy-row backward compat: no
		// escape is possible because filepath.Join does not URL-decode).
		{"percent dot-dot", "foo/%2e%2e/outside.txt", "foo/%2e%2e/outside.txt", true, true},
		{"percent dot", "foo/%2e/inside.txt", "foo/%2e/inside.txt", true, true},
		{"percent dot-dot mixed case", "foo/%2E%2E/outside.txt", "foo/%2E%2E/outside.txt", true, true},
		{"percent dot-dot chain", "foo/%2e%2e/%2e%2e/outside.txt", "foo/%2e%2e/%2e%2e/outside.txt", true, true},
		{"percent nul byte", "foo/%00bar", "foo/%00bar", true, true},
		// chi v5 already URL-decodes most safe percent-encodings
		// (notably `%25` → literal `%`) before the handler sees the path.
		// A segment containing a literal `%` followed by non-hex chars (the
		// post-chi shape of e.g. URL `name%252x/bar` arriving as `name%2x`)
		// must NOT be rejected as "invalid percent-encoding" — the user
		// uploaded a file whose name contains a literal `%`. Strict still
		// rejects `%2e%2e`-style residual traversal because chi preserves
		// `%2e`/`%2E` literally for that exact reason.
		{"literal percent malformed", "foo/%2x/bar", "foo/%2x/bar", false, false},
		{"literal percent name", "name%and.bin", "name%and.bin", false, false},
		{"literal percent at end", "foo/100%/bar", "foo/100%/bar", false, false},
		{"literal percent multiple", "a%b%c.txt", "a%b%c.txt", false, false},
		// Corollary — a segment whose decoded form is NOT traversal
		// is still allowed on both writes and reads. "%2e%2e.txt" decodes
		// to "...txt" (three dots + ".txt"), which is a legal filename.
		{"percent non-traversal", "weird/%2e%2e.txt", "weird/%2e%2e.txt", false, false},
		// Length caps apply regardless of strictness.
		{"oversize segment", "short/" + longSeg, "", true, false},
		{"segment at limit", "short/" + longSegEdge, "short/" + longSegEdge, false, false},
		{"total path over cap", totalOverflow, "", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name+"/strict", func(t *testing.T) {
			got, err := validateRawPath(tc.in, true)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected err for %q, got %q", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err for %q: %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("validateRawPath(%q, strict) = %q, want %q", tc.in, got, tc.want)
			}
		})
		t.Run(tc.name+"/lenient", func(t *testing.T) {
			got, err := validateRawPath(tc.in, false)
			// Lenient mode accepts strictOnly cases but still rejects
			// structural failures (empty, literal `..`, NUL, overlong).
			if tc.wantErr && !tc.strictOnly {
				if err == nil {
					t.Fatalf("expected err for %q, got %q", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err for %q: %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("validateRawPath(%q, lenient) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
