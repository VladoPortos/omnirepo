package pypi_test

import (
	"testing"

	"github.com/dxc-internal/omnirepo/internal/protocol/pypi"
)

func TestNormalize(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"Flask", "flask"},
		{"zope.interface", "zope-interface"},
		{"Twisted__Core", "twisted-core"},
		{"a.b-c_d", "a-b-c-d"},
		{"A.B_C-D", "a-b-c-d"},
		{"already-normal", "already-normal"},
		{"UPPER", "upper"},
		{"dots...dots", "dots-dots"},
		{"mix-_.of.separators", "mix-of-separators"},
		{"", ""},
	}
	for _, c := range cases {
		got := pypi.Normalize(c.in)
		if got != c.want {
			t.Errorf("Normalize(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNormalizeIdempotent(t *testing.T) {
	inputs := []string{"Flask", "Zope.Interface", "A__B", "a-b-c-d", "DOTS...A"}
	for _, in := range inputs {
		once := pypi.Normalize(in)
		twice := pypi.Normalize(once)
		if once != twice {
			t.Errorf("not idempotent on %q: once=%q twice=%q", in, once, twice)
		}
	}
}
