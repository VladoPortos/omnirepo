package pypi

// White-box table-driven coverage for simpleBaseURL() — complements the
// black-box TestPyPIUpstreamTrailingSimpleIsIdempotent regression in
// upstream_parse_test.go. Phase 9 POLISH-05 Codex follow-up (Q3):
// regression test covered the three canonical forms (bare, /simple,
// /simple/) but not sub-path mirrors or hostnames that happen to
// contain the substring "simple".

import "testing"

func TestSimpleBaseURLEdgeCases(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		// Canonical forms (smoke — duplicate of black-box coverage).
		{"https://pypi.org", "https://pypi.org"},
		{"https://pypi.org/", "https://pypi.org"},
		{"https://pypi.org/simple", "https://pypi.org"},
		{"https://pypi.org/simple/", "https://pypi.org"},

		// Hostname or path segment that HAPPENS to contain the
		// substring "simple" but is not the PEP 503 root. Must NOT be
		// stripped.
		{"https://pypi.org/simple-extra/", "https://pypi.org/simple-extra"},
		{"https://pypi.simplesite.example/", "https://pypi.simplesite.example"},

		// Sub-path mirror: operator hosts a PEP 503 index under a
		// custom prefix. The trailing `/simple[/]?` is still the
		// Simple-API root and MUST be stripped.
		{"https://mirror.example/foo/simple", "https://mirror.example/foo"},
		{"https://mirror.example/foo/simple/", "https://mirror.example/foo"},

		// Sub-path WITHOUT `/simple` — stripping rule must no-op.
		{"https://mirror.example/foo/", "https://mirror.example/foo"},
	}
	for _, tc := range cases {
		got := simpleBaseURL(tc.in)
		if got != tc.want {
			t.Errorf("simpleBaseURL(%q) = %q; want %q", tc.in, got, tc.want)
		}
	}
}
