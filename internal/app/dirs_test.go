package app_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dxc-internal/omnirepo/internal/app"
)

// All D-08 subdirectories that EnsureDirs must create (14 entries).
var expectedDirs = []struct {
	rel  string
	mode os.FileMode
}{
	{"config", 0o700},
	{"certs", 0o750},
	{"certs/uploaded", 0o750},
	{"db", 0o750},
	{"blobs", 0o750},
	{"repos", 0o750},
	{"s3", 0o750},
	{"trash", 0o750},
	{"trivy", 0o750},
	{"trivy/db", 0o750},
	{"trivy/cache", 0o750},
	{"sboms", 0o750},
	{"logs", 0o750},
	{"tmp", 0o750},
}

func TestEnsureDirsCreatesAllD08Subdirs(t *testing.T) {
	root := t.TempDir()
	if err := app.EnsureDirs(root); err != nil {
		t.Fatalf("EnsureDirs: %v", err)
	}

	// Every entry in expectedDirs must exist with the correct mode.
	for _, d := range expectedDirs {
		p := filepath.Join(root, d.rel)
		info, err := os.Stat(p)
		if err != nil {
			t.Errorf("missing subdir %q: %v", d.rel, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("%q exists but is not a directory", d.rel)
			continue
		}
		gotMode := info.Mode().Perm()
		if gotMode != d.mode {
			t.Errorf("%q mode = %#o, want %#o", d.rel, gotMode, d.mode)
		}
	}
}

func TestEnsureDirsIdempotent(t *testing.T) {
	root := t.TempDir()
	if err := app.EnsureDirs(root); err != nil {
		t.Fatalf("first EnsureDirs: %v", err)
	}
	// Second call must succeed without error and preserve modes.
	if err := app.EnsureDirs(root); err != nil {
		t.Fatalf("second EnsureDirs: %v", err)
	}
	info, err := os.Stat(filepath.Join(root, "config"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Errorf("config mode after 2nd call = %#o, want 0o700", info.Mode().Perm())
	}
}

func TestEnsureDirsDoesNotPreCreateBlobsSha256(t *testing.T) {
	root := t.TempDir()
	if err := app.EnsureDirs(root); err != nil {
		t.Fatalf("EnsureDirs: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "blobs", "sha256")); !os.IsNotExist(err) {
		t.Errorf("blobs/sha256 must NOT be pre-created (lazy by CAS); stat err = %v", err)
	}
}

func TestEnsureDirsRejectsRootAsFile(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(root, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := app.EnsureDirs(root)
	if err == nil {
		t.Fatal("EnsureDirs: want error when root is a regular file, got nil")
	}
	// Error should name the offending path.
	if !contains(err.Error(), root) {
		t.Errorf("error %q does not name the path %q", err.Error(), root)
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	}()))
}
