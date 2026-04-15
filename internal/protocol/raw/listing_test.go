package raw

import (
	"testing"
)

func TestValidateRawPath(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"foo.txt", "foo.txt", false},
		{"a/b/c.txt", "a/b/c.txt", false},
		{"/leading/slash", "leading/slash", false},
		{"trailing/", "", true}, // empty trailing segment
		{"", "", true},
		{"..", "", true},
		{"a/../b", "", true},
		{"./a", "", true},
		{"a/./b", "", true},
		{"a//b", "", true},        // empty middle segment
		{"a/\x00/b", "", true},    // NUL byte
		{"a/b/../../c", "", true}, // multi-level traversal
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := validateRawPath(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected err for %q, got %q", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err for %q: %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("validateRawPath(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
