package storage_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/dxc-internal/omnirepo/internal/storage"
)

func TestWriteAndRenameHappyPath(t *testing.T) {
	root := t.TempDir()
	tmpDir := filepath.Join(root, "tmp")
	dst := filepath.Join(root, "sub", "hello.txt")

	n, err := storage.WriteAndRename(context.Background(), tmpDir, dst, bytes.NewReader([]byte("hello")))
	if err != nil {
		t.Fatalf("WriteAndRename: %v", err)
	}
	if n != 5 {
		t.Fatalf("bytes written = %d, want 5", n)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("dst content = %q, want %q", got, "hello")
	}

	// tmp file must not linger
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatalf("read tmpDir: %v", err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			t.Fatalf("stray temp file remains: %s", e.Name())
		}
	}
}

func TestWriteAndRenameFailureCleansTemp(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root — chmod 0500 will not block rename")
	}
	root := t.TempDir()
	tmpDir := filepath.Join(root, "tmp")
	// Pre-create the dst parent dir read-only so rename fails.
	dstDir := filepath.Join(root, "ro")
	if err := os.MkdirAll(dstDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dstDir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(dstDir, 0o750) }()

	dst := filepath.Join(dstDir, "x.txt")
	_, err := storage.WriteAndRename(context.Background(), tmpDir, dst, bytes.NewReader([]byte("hi")))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	// tmp file must be cleaned up
	entries, _ := os.ReadDir(tmpDir)
	for _, e := range entries {
		if !e.IsDir() {
			t.Fatalf("stray temp file remains after error: %s", e.Name())
		}
	}
	// dst must not exist
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatalf("dst exists after failure: err=%v", err)
	}
}

// TestSwapDirHappyPath pins the successful rename-aside → rename-in →
// remove-backup dance.
func TestSwapDirHappyPath(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "new")
	dst := filepath.Join(root, "live")
	if err := os.MkdirAll(filepath.Join(src, "inner"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "inner", "marker"), []byte("NEW"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dst, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, "old"), []byte("OLD"), 0o640); err != nil {
		t.Fatal(err)
	}

	if err := storage.SwapDir(src, dst); err != nil {
		t.Fatalf("SwapDir: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dst, "inner", "marker"))
	if err != nil {
		t.Fatalf("read post-swap marker: %v", err)
	}
	if string(got) != "NEW" {
		t.Fatalf("dst marker = %q, want NEW", got)
	}
	// Old file should be gone.
	if _, err := os.Stat(filepath.Join(dst, "old")); !os.IsNotExist(err) {
		t.Fatalf("old file survived swap: err=%v", err)
	}
	// No .old-* backup should linger.
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		if e.Name() != "live" {
			t.Fatalf("stray entry after swap: %s", e.Name())
		}
	}
}

// TestSwapDirRestoresOldOnFailure proves the fix for audit finding #6:
// if rename-in fails after the old dir is moved aside, the old dir MUST be
// restored so the live location is never missing. Forcing a failure: the
// source dir doesn't exist, so os.Rename returns ENOENT after the backup
// step.
func TestSwapDirRestoresOldOnFailure(t *testing.T) {
	root := t.TempDir()
	dst := filepath.Join(root, "live")
	if err := os.MkdirAll(dst, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, "marker"), []byte("OLD"), 0o640); err != nil {
		t.Fatal(err)
	}

	// Non-existent src forces the rename-in step to fail.
	err := storage.SwapDir(filepath.Join(root, "does-not-exist"), dst)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	// Old dir must still be at dst.
	got, readErr := os.ReadFile(filepath.Join(dst, "marker"))
	if readErr != nil {
		t.Fatalf("old dir not restored: %v", readErr)
	}
	if string(got) != "OLD" {
		t.Fatalf("old marker = %q, want OLD", got)
	}
	// No .old-* backup should linger.
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		if e.Name() != "live" {
			t.Fatalf("stray entry after failed swap: %s", e.Name())
		}
	}
}

// TestSwapDirNoPriorDst covers the fresh-install case where dst doesn't yet
// exist. SwapDir should just rename src into place without any backup.
func TestSwapDirNoPriorDst(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "new")
	dst := filepath.Join(root, "live")
	if err := os.MkdirAll(src, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "m"), []byte("hi"), 0o640); err != nil {
		t.Fatal(err)
	}

	if err := storage.SwapDir(src, dst); err != nil {
		t.Fatalf("SwapDir: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dst, "m"))
	if err != nil {
		t.Fatalf("read post-swap: %v", err)
	}
	if string(got) != "hi" {
		t.Fatalf("content = %q, want hi", got)
	}
}

func TestWriteAndRenameDoesNotCorruptExistingDst(t *testing.T) {
	root := t.TempDir()
	tmpDir := filepath.Join(root, "tmp")
	dst := filepath.Join(root, "sub", "hello.txt")
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("original"), 0o640); err != nil {
		t.Fatal(err)
	}
	// Pre-existing dst; after a successful call it should hold new content
	// (atomic replace). If we never called Rename the dst stays intact —
	// this test exercises the positive path: Rename succeeds, dst replaced,
	// tmp cleaned up (ie never a truncated/partial file at dst).
	_, err := storage.WriteAndRename(context.Background(), tmpDir, dst, bytes.NewReader([]byte("NEW")))
	if err != nil {
		t.Fatalf("WriteAndRename: %v", err)
	}
	got, _ := os.ReadFile(dst)
	if string(got) != "NEW" {
		t.Fatalf("dst = %q, want NEW", got)
	}
}
