package jobs

// White-box tests for sanitizeJobError, the scrub + truncate helper the
// pool applies to handler errors before they land in sync_jobs.last_error.
// Lives in the jobs package (not jobs_test) so the unexported helper is
// callable directly.

import (
	"errors"
	"strings"
	"testing"
)

func TestSanitizeJobError_ScrubsAuthHeader(t *testing.T) {
	err := errors.New(`request failed: Authorization: Basic YWxpY2U6c2VrcmV0 rejected by upstream`)
	got := sanitizeJobError(err)
	if strings.Contains(got, "YWxpY2U6c2VrcmV0") {
		t.Fatalf("credential not scrubbed: %q", got)
	}
	if !strings.Contains(got, "Authorization: REDACTED") {
		t.Fatalf("no REDACTED marker: %q", got)
	}
}

func TestSanitizeJobError_ScrubsFilesystemPath(t *testing.T) {
	err := errors.New(`open /var/lib/omnirepo/repos/proj/deb/repo/pool/main/hello.deb: no such file`)
	got := sanitizeJobError(err)
	if strings.Contains(got, "/var/lib/omnirepo") {
		t.Fatalf("filesystem path leaked: %q", got)
	}
	if !strings.Contains(got, "[path]") {
		t.Fatalf("no [path] marker: %q", got)
	}
}

func TestSanitizeJobError_ScrubsTmpPath(t *testing.T) {
	err := errors.New(`tar: /tmp/omnirepo-ingest-123/chart.tgz: unexpected EOF`)
	got := sanitizeJobError(err)
	if strings.Contains(got, "/tmp/omnirepo-ingest-123") {
		t.Fatalf("/tmp path leaked: %q", got)
	}
}

func TestSanitizeJobError_TruncatesOversized(t *testing.T) {
	// Construct a 100 KiB error message.
	huge := strings.Repeat("X", 100*1024)
	err := errors.New(huge)
	got := sanitizeJobError(err)
	if len(got) > MaxLastErrorLen {
		t.Fatalf("persisted length %d > MaxLastErrorLen %d", len(got), MaxLastErrorLen)
	}
	if !strings.HasSuffix(got, "...[truncated]") {
		t.Fatalf("missing truncation marker: suffix=%q", got[len(got)-20:])
	}
}

func TestSanitizeJobError_ShortErrorPassthrough(t *testing.T) {
	err := errors.New("upstream returned 503")
	got := sanitizeJobError(err)
	if got != "upstream returned 503" {
		t.Fatalf("short error mutated: %q", got)
	}
}

func TestSanitizeJobError_NilReturnsEmpty(t *testing.T) {
	if got := sanitizeJobError(nil); got != "" {
		t.Fatalf("nil → %q; want empty", got)
	}
}
