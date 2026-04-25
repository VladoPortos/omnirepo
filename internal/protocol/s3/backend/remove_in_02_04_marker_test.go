package backend_test

import (
	"os"
	"strings"
	"testing"
)

// TestRemoveIn0204MarkerPresent is the W-2 forcing function (Plan 02-02
// threat-register entry T-02-02-05): Plan 02-02 leaves a `// REMOVE IN 02-04`
// marker at the legacy multipartInitiatorFallback call site so Plan 02-04's
// TestNoMultipartInitiatorFallbackResidue grep gate has a concrete cleanup
// target. This test simply asserts the marker exists — it will fail in 02-04
// once the constant + marker are removed, at which point Plan 02-04 deletes
// this test file too.
//
// Why both strings get checked: 02-04's gate fails the build if EITHER
// `multipartInitiatorFallback` OR `// REMOVE IN 02-04` survives. By
// asserting both here we make the gate symmetric: 02-02 cannot ship without
// the marker, and 02-04 cannot ship with the marker.
func TestRemoveIn0204MarkerPresent(t *testing.T) {
	data, err := os.ReadFile("multipart.go")
	if err != nil {
		t.Fatalf("read multipart.go: %v", err)
	}
	src := string(data)
	if !strings.Contains(src, "REMOVE IN 02-04") {
		t.Fatal("expected `// REMOVE IN 02-04` marker in multipart.go (W-2 forcing function for Plan 02-04 cleanup)")
	}
	if !strings.Contains(src, "multipartInitiatorFallback") {
		t.Fatal("expected multipartInitiatorFallback constant to still exist in multipart.go (Plan 02-04 removes it)")
	}
}
