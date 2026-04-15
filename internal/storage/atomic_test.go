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
