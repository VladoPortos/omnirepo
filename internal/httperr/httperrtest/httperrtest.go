// Package httperrtest holds test-only leak-screening helpers for the
// httperr envelope contract. It lives outside the production package so
// the shipped binary carries no test infrastructure (mirrors the
// metadata/sqlitetest convention).
package httperrtest

import "regexp"

// internalMarkers flags strings that look like internal-only leakage
// (filesystem paths, Go driver messages, stack markers). Used by tests
// (e.g. the api handlers envelope integration test) to assert
// Envelope.Message / Envelope.Hint never carry these substrings.
var internalMarkers = []*regexp.Regexp{
	// Filesystem absolute paths (matches both leading and mid-string).
	regexp.MustCompile(`(^|\s)/[a-zA-Z0-9_/.-]+`),
	// Go source locations (e.g. "file.go:123").
	regexp.MustCompile(`\.go:\d+`),
	// Stack markers — the word "goroutine" or a runtime.* frame.
	regexp.MustCompile(`\b(goroutine|runtime\.)`),
	// Go driver / syscall leaks. Note: "sqlite" is matched as a bare
	// substring because the driver appears in messages like "sqlite3 ...".
	regexp.MustCompile(`sqlite`),
	regexp.MustCompile(`\b(sql:|read:|open:|stat:)`),
}

// IsInternalString reports whether s contains substrings that look like
// internal-only information (paths, source locations, stack markers,
// driver leaks). Used by leak-screening tests (api envelope integration
// gate) to prevent internal-string leaks reaching wire envelopes.
func IsInternalString(s string) bool {
	for _, re := range internalMarkers {
		if re.MatchString(s) {
			return true
		}
	}
	return false
}
