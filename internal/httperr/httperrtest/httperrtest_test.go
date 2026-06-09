package httperrtest_test

import (
	"testing"

	"github.com/vladoportos/omnirepo/internal/httperr/httperrtest"
)

func TestIsInternalString_DetectsLeakage(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"/foo/bar", true},
		{"failed: .go:123", true},
		{"sqlite3 error", true},
		{"sqlite", true},
		{"goroutine 1 [running]", true},
		{"runtime.something", true},
		{"read: connection reset", true},
		{"open: no such file", true},
		{"stat: not found", true},
		{"Name is required", false},
		{"You do not have permission", false},
		{"Trivy database not installed.", false},
		{"", false},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got := httperrtest.IsInternalString(c.in)
			if got != c.want {
				t.Errorf("IsInternalString(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}
