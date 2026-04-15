package auth_test

import (
	"strings"
	"testing"

	"github.com/dxc-internal/omnirepo/internal/auth"
)

func TestProjectNameValid_Accepts(t *testing.T) {
	cases := []string{"dxc", "acme-2", "foo.bar", "team_1", "a", "a1", "abc123"}
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
