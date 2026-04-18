package deb

import (
	"os"
	"path/filepath"
	"testing"
)

// TestResolvePoolPath_ReadsReleaseFile verifies that ResolvePoolPath reads
// the repo's dists/<suite>/Release and honors its Components: line (picking
// the first component for pool layout).
func TestResolvePoolPath_ReadsReleaseFile(t *testing.T) {
	tmp := t.TempDir()
	distsDir := filepath.Join(tmp, "proj", "deb", "r", "dists", "stable")
	if err := os.MkdirAll(distsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Release file declares two components — expect "main" (first) in path.
	releaseBody := []byte("Suite: stable\nComponents: main contrib\nArchitectures: amd64 arm64\nDate: Wed, 01 Jan 2026 00:00:00 UTC\n")
	if err := os.WriteFile(filepath.Join(distsDir, "Release"), releaseBody, 0o644); err != nil {
		t.Fatal(err)
	}

	ctrl := &Control{Package: "foo"}
	got := ResolvePoolPath(tmp, "proj", "r", "stable", "foo_1.0_amd64.deb", ctrl)
	want := "pool/main/f/foo/foo_1.0_amd64.deb"
	if got != want {
		t.Errorf("ResolvePoolPath (Release-aware, first component) = %q; want %q", got, want)
	}
}

// TestResolvePoolPath_CustomComponent verifies that a Release file declaring
// a non-default first component (e.g. "contrib") is honored in the pool path.
func TestResolvePoolPath_CustomComponent(t *testing.T) {
	tmp := t.TempDir()
	distsDir := filepath.Join(tmp, "proj", "deb", "r", "dists", "stable")
	if err := os.MkdirAll(distsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// First component is "contrib" — path should honor it.
	releaseBody := []byte("Suite: stable\nComponents: contrib main\n")
	if err := os.WriteFile(filepath.Join(distsDir, "Release"), releaseBody, 0o644); err != nil {
		t.Fatal(err)
	}

	got := ResolvePoolPath(tmp, "proj", "r", "stable", "bar_2.0.deb", &Control{Package: "bar"})
	want := "pool/contrib/b/bar/bar_2.0.deb"
	if got != want {
		t.Errorf("custom component: got %q want %q", got, want)
	}
}

// TestResolvePoolPath_FallsBackWhenReleaseMissing verifies that the legacy
// filename-based inference (pool/main/<initial>/<pkg>/<filename>) kicks in
// when the Release file is absent.
func TestResolvePoolPath_FallsBackWhenReleaseMissing(t *testing.T) {
	tmp := t.TempDir()
	got := ResolvePoolPath(tmp, "proj", "r", "stable", "baz_1.0.deb", &Control{Package: "baz"})
	want := "pool/main/b/baz/baz_1.0.deb"
	if got != want {
		t.Errorf("fallback (missing Release): got %q want %q", got, want)
	}
}

// TestResolvePoolPath_FallsBackOnMalformedRelease verifies that a Release
// file missing a Components: line (or otherwise malformed) falls back to
// filename inference rather than returning an empty component.
func TestResolvePoolPath_FallsBackOnMalformedRelease(t *testing.T) {
	tmp := t.TempDir()
	distsDir := filepath.Join(tmp, "proj", "deb", "r", "dists", "stable")
	if err := os.MkdirAll(distsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Release file with no Components: line.
	releaseBody := []byte("Suite: stable\nArchitectures: amd64\n")
	if err := os.WriteFile(filepath.Join(distsDir, "Release"), releaseBody, 0o644); err != nil {
		t.Fatal(err)
	}

	got := ResolvePoolPath(tmp, "proj", "r", "stable", "qux_1.0.deb", &Control{Package: "qux"})
	want := "pool/main/q/qux/qux_1.0.deb"
	if got != want {
		t.Errorf("fallback (no Components): got %q want %q", got, want)
	}
}

// TestResolvePoolPath_RejectsTraversalInComponent verifies T-07-06-01: a
// malicious Release file with a component containing "/" or ".." is
// rejected (falls back to "main") to prevent path traversal out of the
// pool/ tree.
func TestResolvePoolPath_RejectsTraversalInComponent(t *testing.T) {
	tmp := t.TempDir()
	distsDir := filepath.Join(tmp, "proj", "deb", "r", "dists", "stable")
	if err := os.MkdirAll(distsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Adversarial Release file: crafted component contains traversal bytes.
	releaseBody := []byte("Components: ../evil main\n")
	if err := os.WriteFile(filepath.Join(distsDir, "Release"), releaseBody, 0o644); err != nil {
		t.Fatal(err)
	}

	got := ResolvePoolPath(tmp, "proj", "r", "stable", "foo_1.0.deb", &Control{Package: "foo"})
	// Must NOT contain ".." — should fall back to legacy "main".
	want := "pool/main/f/foo/foo_1.0.deb"
	if got != want {
		t.Errorf("traversal rejection: got %q want %q", got, want)
	}
}

// TestResolvePoolPath_NilControl verifies that a nil Control struct (or one
// with an empty Package field) still produces a sensible fallback path
// ("pool/<component>/x/x/<filename>"), preserving the legacy relPoolPath
// contract.
func TestResolvePoolPath_NilControl(t *testing.T) {
	tmp := t.TempDir()
	got := ResolvePoolPath(tmp, "proj", "r", "stable", "file.deb", nil)
	want := "pool/main/x/x/file.deb"
	if got != want {
		t.Errorf("nil ctrl: got %q want %q", got, want)
	}
}
