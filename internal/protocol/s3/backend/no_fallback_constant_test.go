package backend_test

// Plan 02-04 Task 1 — grep gate: TestNoMultipartInitiatorFallbackResidue.
//
// Symmetric counterpart to Plan 02-02's TestRemoveIn0204MarkerPresent
// (which asserts both `multipartInitiatorFallback` AND `// REMOVE IN 02-04`
// EXIST in multipart.go). This test asserts both are GONE — across the
// entire internal/ tree — once Plan 02-04 ships. Build fails if either
// string survives anywhere under internal/.
//
// Walks the file tree in-process rather than shelling out to grep so
// the test runs identically on every platform CI cares about.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoMultipartInitiatorFallbackResidue verifies that Plan 02-04 has
// removed both the legacy fallback constant and the // REMOVE IN 02-04
// marker comment installed by Plan 02-02. The build fails if either
// string survives anywhere under the project's internal/ tree.
//
// Runs from internal/protocol/s3/backend so the relative path back to
// internal/ is "../../../..". We resolve it absolutely via filepath.Abs
// so the lookup is robust to test working-dir changes.
func TestNoMultipartInitiatorFallbackResidue(t *testing.T) {
	internalDir, err := filepath.Abs(filepath.Join("..", "..", "..", "..", "internal"))
	if err != nil {
		t.Fatalf("resolve internal/: %v", err)
	}
	if st, err := os.Stat(internalDir); err != nil || !st.IsDir() {
		t.Fatalf("internal/ not a dir at %s: %v", internalDir, err)
	}

	needles := []string{"multipartInitiatorFallback", "REMOVE IN 02-04"}

	for _, needle := range needles {
		var hits []string
		err := filepath.WalkDir(internalDir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			// Skip this very test file (it has to mention the strings to
			// gate them) and the symmetric Plan 02-02 marker test (which
			// Plan 02-04 deletes — see Task 1 step below).
			base := filepath.Base(path)
			if base == "no_fallback_constant_test.go" {
				return nil
			}
			data, rerr := os.ReadFile(path)
			if rerr != nil {
				return rerr
			}
			if strings.Contains(string(data), needle) {
				rel, _ := filepath.Rel(internalDir, path)
				hits = append(hits, rel)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk internal/: %v", err)
		}
		if len(hits) > 0 {
			t.Errorf("found residual %q in:\n  %s", needle, strings.Join(hits, "\n  "))
		}
	}
}
