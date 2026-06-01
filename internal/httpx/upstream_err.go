// Package httpx — shared upstream error scrubbing.
//
// Sync handlers for rpm/deb/pypi/helm all talk to upstream HTTP servers and
// can end up with Authorization header bytes embedded in wrapped error
// strings (go libraries sometimes format request dumps into errors).
// SanitizeUpstreamErr strips those bytes before the error lands in
// sync_jobs.last_error.
//
// Kept in internal/httpx so it's reusable across protocol packages without
// forcing a circular import through internal/protocol/oci (which owns its
// own copy).
package httpx

import (
	"errors"
	"regexp"

	"github.com/vladoportos/omnirepo/internal/streamio"
)

// authRegex matches "Authorization: <bytes-to-EOL-or-newline-or-quote>"
// case-insensitively. The greedy tail-match stops at \r, \n, ", or '.
var authRegex = regexp.MustCompile(`(?i)Authorization:\s*[^\r\n"']*`)

// classifiedError wraps a scrubbed message string with a clean sentinel
// for errors.Is propagation. The sentinel itself NEVER contains
// credential bytes (it's a package-level errors.New from streamio), so
// preserving the wrap chain is safe.
type classifiedError struct {
	msg      string
	sentinel error
}

func (e *classifiedError) Error() string { return e.msg }
func (e *classifiedError) Unwrap() error { return e.sentinel }

// SanitizeUpstreamErr returns a new error whose Error() has any
// Authorization header bytes replaced with "Authorization: REDACTED".
//
// The wrap chain of the input is dropped (nested %w values can retain
// credential bytes inside their own formatted strings).
// However, well-known sentinels (streamio.ErrArtifactTooLarge,
// streamio.ErrMetadataTooLarge — both credential-free errors.New
// values) are preserved via a typed wrapper so callers can still use
// errors.Is to detect over-limit conditions. New
// sentinels need to be added to this allow-list explicitly.
func SanitizeUpstreamErr(err error) error {
	if err == nil {
		return nil
	}
	scrubbed := authRegex.ReplaceAllString(err.Error(), "Authorization: REDACTED")
	for _, sentinel := range []error{
		streamio.ErrArtifactTooLarge,
		streamio.ErrMetadataTooLarge,
	} {
		if errors.Is(err, sentinel) {
			return &classifiedError{msg: scrubbed, sentinel: sentinel}
		}
	}
	return errors.New(scrubbed)
}
