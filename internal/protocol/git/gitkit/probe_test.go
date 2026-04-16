//go:build probe

// Phase 04-01 Wave-0 probe: confirms sosedoff/gitkit v0.4.0 compiles under
// Go 1.25 and exposes the fields Plan 09 will rely on for the HTTP fallback.
//
// Fields referenced (must continue to exist for Plan 09):
//   - gitkit.Config{}          — server configuration struct
//   - gitkit.New               — constructor returning *gitkit.Server
//   - gitkit.Receiver{}        — post-receive hook plumbing
//
// File is named probe_test.go (NOT _probe_test.go) because the Go build
// system silently ignores files whose name begins with '_' or '.'; an
// underscore-prefixed test file cannot be discovered by `go test`. This
// is recorded as a Rule-1 deviation in 04-01-SUMMARY.md.

package gitkit_probe

import (
	"testing"

	gk "github.com/sosedoff/gitkit"
)

func TestGitkitCompiles(t *testing.T) {
	var _ gk.Config         // struct exists
	var _ = gk.New          // constructor exists
	var _ gk.Receiver       // receiver plumbing exists
	_ = (*gk.Server)(nil)   // server type exists
}
