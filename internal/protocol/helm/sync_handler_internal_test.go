package helm

import (
	"errors"
	"testing"
)

// TestIsNonChartManifestErr pins the Helm SDK / ORAS error-string match
// that lets fetchAndCommitOCI skip non-chart OCI sidecar manifests
// (Bitnami `-metadata`, future conventions) instead of aborting the
// whole sync batch. Changes to the upstream error wording require
// updating both the matcher and this test.
func TestIsNonChartManifestErr(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil error", nil, false},
		{"helm sdk wording", errors.New("manifest does not contain minimum number of descriptors (2), descriptors found: 1"), true},
		{"wrapped", errors.New("ociclient: pull oci://.../nginx:22.0.7-metadata: manifest does not contain minimum number of descriptors"), true},
		{"unrelated auth error", errors.New("401 Unauthorized"), false},
		{"unrelated network error", errors.New("connection reset by peer"), false},
	}
	for _, tc := range cases {
		if got := isNonChartManifestErr(tc.err); got != tc.want {
			t.Errorf("%s: isNonChartManifestErr(%v) = %v, want %v", tc.name, tc.err, got, tc.want)
		}
	}
}
