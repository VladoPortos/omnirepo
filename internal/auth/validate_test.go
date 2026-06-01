package auth_test

import (
	"strings"
	"testing"

	"github.com/vladoportos/omnirepo/internal/auth"
)

func TestProjectNameValid_Accepts(t *testing.T) {
	cases := []string{"acme", "acme-2", "foo.bar", "team_1", "a", "a1", "abc123"}
	for _, c := range cases {
		if err := auth.ProjectNameValid(c); err != nil {
			t.Errorf("ProjectNameValid(%q): %v; want nil", c, err)
		}
	}
}

func TestProjectNameValid_RejectsAllReservedPrefixes(t *testing.T) {
	reserved := []string{"v2", "s3", "git", "api", "ui", "assets", "static", "login", "logout", "healthz", "readyz"}
	for _, r := range reserved {
		err := auth.ProjectNameValid(r)
		if err == nil {
			t.Errorf("ProjectNameValid(%q): err=nil; want reserved error", r)
			continue
		}
		if !strings.Contains(err.Error(), "reserved") {
			t.Errorf("ProjectNameValid(%q): error %q does not mention reserved", r, err.Error())
		}
	}
}

func TestProjectNameValid_Rejects(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"uppercase S3", "S3"},
		{"leading hyphen", "-foo"},
		{"leading underscore", "_foo"},
		{"64-char overflow", strings.Repeat("a", 64)},
		{"slash", "foo/bar"},
		{"unicode", "café"},
		{"uppercase letter", "Foo"},
	}
	for _, c := range cases {
		if err := auth.ProjectNameValid(c.in); err == nil {
			t.Errorf("ProjectNameValid(%s=%q): err=nil; want error", c.name, c.in)
		}
	}
}

func TestLoginValid_Accepts(t *testing.T) {
	cases := []string{"alice", "bob.jones", "charlie-1", "Alice", "aB1"}
	for _, c := range cases {
		if err := auth.LoginValid(c); err != nil {
			t.Errorf("LoginValid(%q): %v; want nil", c, err)
		}
	}
}

func TestLoginValid_Rejects(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"unicode", "café"},
		{"65-char overflow", strings.Repeat("a", 65)},
		{"at sign", "foo@bar"},
		{"space", "alice bob"},
		{"slash", "a/b"},
	}
	for _, c := range cases {
		if err := auth.LoginValid(c.in); err == nil {
			t.Errorf("LoginValid(%s=%q): err=nil; want error", c.name, c.in)
		}
	}
}

// The policy floor must be uniform across setup,
// self-service change, and admin force-reset. Pin it here so any
// future drift fails noisily before reaching production.
func TestPasswordValid(t *testing.T) {
	cases := []struct {
		pw      string
		wantErr bool
	}{
		{"", true},
		{"a", true},
		{"abc", true},
		{"1234567", true},         // 7 chars: floor minus one
		{"12345678", false},       // 8 chars: at floor
		{"Adm1n!Passw0rd", false}, // realistic
		{strings.Repeat("x", 64), false},
	}
	for _, tc := range cases {
		t.Run(tc.pw, func(t *testing.T) {
			err := auth.PasswordValid(tc.pw)
			if tc.wantErr && err == nil {
				t.Errorf("PasswordValid(%q): err=nil; want error", tc.pw)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("PasswordValid(%q): %v; want nil", tc.pw, err)
			}
		})
	}
}
