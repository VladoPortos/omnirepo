// Package protocoltest hosts cross-package invariants over the protocol
// handler tree (internal/protocol/**).
//
// TestNoPercentVLeakInHTTPError enforces that no protocol
// handler may emit a formatted Go error value through http.Error's client
// body. Handlers must log the real error via slog.ErrorContext (keyed by the
// chi request id / X-Incident-Id) and emit a static generic client message.
//
// Rationale: protocol clients (dockerd, apt-get, pip, curl, aws-cli, git)
// don't parse JSON error envelopes, so the /api/v1 ApiErrorEnvelope isn't
// the right fix for them. The correct fix is to stop interpolating internal
// strings (filesystem paths, driver errors, goroutine traces) into the wire
// response, while keeping the protocol-native error shape (OCI JSON, APT
// plain text, S3 XML) intact.
//
// The Makefile ships an equivalent grep gate in `make lint-protocol-redaction`
// for out-of-process CI. This Go test runs in `go test ./...` so it catches
// regressions at merge time before they reach CI.
package protocoltest

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// repoRoot resolves the module root by walking up from this file until it
// finds go.mod. Works regardless of where `go test` is invoked from.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, here, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(here)
	for i := 0; i < 10; i++ {
		candidate := filepath.Join(dir, "go.mod")
		if _, err := exec.Command("test", "-f", candidate).CombinedOutput(); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	t.Fatalf("could not locate go.mod walking up from %s", here)
	return ""
}

// TestNoPercentVLeakInHTTPError greps the internal/protocol tree for
// http.Error call sites that carry a %v-interpolated value and fails if any
// are present. It's the runtime mirror of the Makefile `lint-protocol-
// redaction` gate, so `go test ./...` fails the same way `make test` does.
//
// Exclusions:
//   - *_test.go is excluded: test fixtures legitimately use http.Error with
//     %v when faking a broken upstream or asserting a leak-before-fix state.
func TestNoPercentVLeakInHTTPError(t *testing.T) {
	root := repoRoot(t)
	target := filepath.Join(root, "internal", "protocol")

	// grep -rnE with --include/--exclude matches the Makefile gate exactly so
	// both checks pass or fail in lockstep.
	cmd := exec.Command("grep", "-rnE",
		"--include=*.go",
		"--exclude=*_test.go",
		`http\.Error\([^)]*%v`,
		target,
	)
	out, err := cmd.Output()
	// grep exits 1 when no matches found — that's the clean case.
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
			return // clean
		}
		t.Fatalf("grep failed: %v (stderr=%s)", err, string(ee(err).Stderr))
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return // clean (edge case: grep wrote nothing but exited 0)
	}
	t.Fatalf("redaction leak: %d http.Error(...) sites in internal/protocol/** "+
		"still interpolate %%v of a Go error. Redact via slog.ErrorContext "+
		"+ static generic message. Offenders:\n%s",
		len(lines), strings.Join(lines, "\n"))
}

// ee is a tiny helper for the fatal path above: returns an *exec.ExitError
// with its Stderr populated, or a zero-value one so the format verb works.
func ee(err error) *exec.ExitError {
	if e, ok := err.(*exec.ExitError); ok {
		return e
	}
	return &exec.ExitError{}
}
