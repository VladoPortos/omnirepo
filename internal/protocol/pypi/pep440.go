package pypi

import (
	"fmt"
	"regexp"
)

// pep440.go — PEP 440 public-version-identifier validator.
//
// Derived directly from the PEP 440 BNF (§"Public version identifiers",
// https://peps.python.org/pep-0440/#public-version-identifiers). The pattern
// covers: optional epoch (`N!`), release segment (`N(.N)*`), optional
// pre-release (`[-_.]?(a|b|c|rc|alpha|beta|pre|preview)[-_.]?N?`), optional
// post-release (explicit `[-_.]?(post|rev|r)[-_.]?N?` or implicit legacy
// `-N` form), optional dev-release (`[-_.]?dev[-_.]?N?`), and optional
// local version (`+[a-z0-9]+([-_.][a-z0-9]+)*`). Case-insensitive.
//
// Design intent — Go's `regexp` package is RE2-based (linear time in input
// length regardless of pattern shape). Catastrophic backtracking is
// structurally impossible, so no ReDoS mitigation beyond "use stdlib
// regexp" is required (threat model T-03-01-01). Future contributors must
// NOT migrate this to a backtracking engine (PCRE, etc.).
//
// The pattern is anchored (`^...$`) so partial matches are impossible —
// this is what rejects smuggled content like " 1.0" (leading whitespace)
// or "1.0.0!2.0" (epoch after the release segment). The anchoring is
// locked by negative test rows.
//
// Validate accepts — it does NOT normalize. Callers that need the
// PEP-440-canonical form normalize separately; most upstream code in this
// package treats the filename-supplied version as the canonical identity
// (no dedup across distinct filename spellings — see 03-CONTEXT.md §D-04).
var versionPattern = regexp.MustCompile(
	`^(?i)` +
		`(?:(?P<epoch>[0-9]+)!)?` +
		`(?P<release>[0-9]+(?:\.[0-9]+)*)` +
		`(?P<pre>[-_.]?(?:a|b|c|rc|alpha|beta|pre|preview)[-_.]?[0-9]*)?` +
		`(?P<post>(?:-[0-9]+)|(?:[-_.]?(?:post|rev|r)[-_.]?[0-9]*))?` +
		`(?P<dev>[-_.]?dev[-_.]?[0-9]*)?` +
		`(?:\+(?P<local>[a-z0-9]+(?:[-_.][a-z0-9]+)*))?` +
		`$`,
)

// Validate reports whether v conforms to the PEP 440 "public version
// identifier" grammar. Returns nil on match; otherwise returns
// `fmt.Errorf("pypi: invalid PEP 440 version: %q", v)` — callers wrap
// with their own prefix (pypi_sync: ..., pypi: malformed sdist filename:
// ...).
//
// Error output uses %q so any upstream-supplied bytes (already allowlisted
// at the mirror boundary by isSafeMirrorFilename — F-07.6) are Go-escaped
// before landing in logs. No PII or secret material flows through this
// layer (T-03-01-03).
func Validate(v string) error {
	if !versionPattern.MatchString(v) {
		return fmt.Errorf("pypi: invalid PEP 440 version: %q", v)
	}
	return nil
}
