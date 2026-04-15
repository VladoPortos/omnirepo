// Package httpx — shared upstream error scrubbing (Phase 03 Plan 06, SYNC-05).
//
// Sync handlers for rpm/deb/pypi/helm all talk to upstream HTTP servers and
// can end up with Authorization header bytes embedded in wrapped error
// strings (go libraries sometimes format request dumps into errors).
// SanitizeUpstreamErr strips those bytes before the error lands in
// sync_jobs.last_error (T-03-06-01).
//
// Kept in internal/httpx so it's reusable across protocol packages without
// forcing a circular import through internal/protocol/oci (which owns its
// own copy for Phase 02-10 compatibility).
package httpx

import (
	"errors"
	"regexp"
)

// authRegex matches "Authorization: <bytes-to-EOL-or-newline-or-quote>"
// case-insensitively. The greedy tail-match stops at \r, \n, ", or '.
var authRegex = regexp.MustCompile(`(?i)Authorization:\s*[^\r\n"']*`)

// SanitizeUpstreamErr returns a new error whose Error() has any
// Authorization header bytes replaced with "Authorization: REDACTED".
// The original wrap chain is deliberately dropped — nested %w values can
// retain the credential bytes inside their own formatted strings.
func SanitizeUpstreamErr(err error) error {
	if err == nil {
		return nil
	}
	scrubbed := authRegex.ReplaceAllString(err.Error(), "Authorization: REDACTED")
	return errors.New(scrubbed)
}
