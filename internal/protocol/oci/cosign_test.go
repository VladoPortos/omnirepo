package oci_test

import (
	"testing"

	"github.com/vladoportos/omnirepo/internal/protocol/oci"
)

// TestCosignTagDerivation verifies the sha256:<hex> → sha256-<hex>.sig
// conversion.
func TestCosignTagDerivation(t *testing.T) {
	cases := []struct {
		digest, want string
	}{
		{
			digest: "sha256:abc123",
			want:   "sha256-abc123.sig",
		},
		{
			digest: "sha256:0000000000000000000000000000000000000000000000000000000000000000",
			want:   "sha256-0000000000000000000000000000000000000000000000000000000000000000.sig",
		},
	}
	for _, tc := range cases {
		got := oci.CosignTag(tc.digest)
		if got != tc.want {
			t.Errorf("CosignTag(%q) = %q; want %q", tc.digest, got, tc.want)
		}
	}
}
