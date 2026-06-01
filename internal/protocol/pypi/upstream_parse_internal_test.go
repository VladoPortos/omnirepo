package pypi

import "testing"

// TestIsInstallableExt — only .whl / .tar.gz / .tgz / .zip are installable
// from a PEP 503 simple index in 2026. Mirror sync must skip legacy
// .egg / .exe / .msi entries upstream still serves for some projects so
// the inline version parser in sync_handler.go's collect pass doesn't
// mangle the "version" column.
func TestIsInstallableExt(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"hello-1.0-py3-none-any.whl", true},
		{"hello-1.0.tar.gz", true},
		{"hello-1.0.tgz", true},
		{"hello-1.0.zip", true},
		{"REQUESTS-2.33.1-PY3-none-any.WHL", true}, // case-insensitive

		{"requests-2.23.0-py2.7.egg", false}, // real upstream artefact
		{"package-1.0.win32.exe", false},
		{"package-1.0.msi", false},
		{"package-1.0.rpm", false},
		{"package-1.0.deb", false},
		{"README", false},
	}
	for _, c := range cases {
		if got := isInstallableExt(c.name); got != c.want {
			t.Errorf("isInstallableExt(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}
