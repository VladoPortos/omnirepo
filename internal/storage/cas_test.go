package storage_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/vladoportos/omnirepo/internal/storage"
)

// sha256("hello") = 2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824
const helloDigest = "sha256:2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"

func TestCASKnownHelloDigest(t *testing.T) {
	root := t.TempDir()
	c := storage.NewCAS(root)
	digest, n, err := c.Put(context.Background(), bytes.NewReader([]byte("hello")))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if digest != helloDigest {
		t.Fatalf("digest = %q, want %q", digest, helloDigest)
	}
	if n != 5 {
		t.Fatalf("size = %d, want 5", n)
	}
	// Layout: <root>/sha256/<first2>/<hex>
	hex := helloDigest[len("sha256:"):]
	expected := filepath.Join(root, "sha256", hex[:2], hex)
	info, err := os.Stat(expected)
	if err != nil {
		t.Fatalf("stat expected blob path %q: %v", expected, err)
	}
	if info.Size() != 5 {
		t.Fatalf("blob size on disk = %d, want 5", info.Size())
	}
}

func TestCASPutIdempotent(t *testing.T) {
	root := t.TempDir()
	c := storage.NewCAS(root)
	d1, _, err := c.Put(context.Background(), bytes.NewReader([]byte("hello")))
	if err != nil {
		t.Fatal(err)
	}
	hex := d1[len("sha256:"):]
	p := filepath.Join(root, "sha256", hex[:2], hex)
	info1, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}

	d2, _, err := c.Put(context.Background(), bytes.NewReader([]byte("hello")))
	if err != nil {
		t.Fatal(err)
	}
	if d1 != d2 {
		t.Fatalf("digest differs between calls: %q vs %q", d1, d2)
	}
	info2, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	// Inode-preserving on same filesystem
	if !os.SameFile(info1, info2) {
		t.Fatalf("blob replaced on idempotent Put (mtime/inode changed)")
	}
}

func TestCASGetStatExistsDelete(t *testing.T) {
	root := t.TempDir()
	c := storage.NewCAS(root)
	digest, _, err := c.Put(context.Background(), bytes.NewReader([]byte("hello")))
	if err != nil {
		t.Fatal(err)
	}
	exists, err := c.Exists(context.Background(), digest)
	if err != nil || !exists {
		t.Fatalf("Exists after Put: exists=%v err=%v", exists, err)
	}
	size, exists, err := c.Stat(context.Background(), digest)
	if err != nil || !exists || size != 5 {
		t.Fatalf("Stat: size=%d exists=%v err=%v", size, exists, err)
	}
	rc, err := c.Get(context.Background(), digest)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	_ = rc.Close()
	if string(got) != "hello" {
		t.Fatalf("Get content = %q, want hello", got)
	}
	if err := c.Delete(context.Background(), digest); err != nil {
		t.Fatal(err)
	}
	exists, _ = c.Exists(context.Background(), digest)
	if exists {
		t.Fatal("Exists true after Delete")
	}
}

// TestCASPutFromPath_PromoteTmpFile asserts that PutFromPath hashes the
// file's contents, places it at the canonical blob path, and consumes the
// source file (atomic rename — no second io.Copy).
func TestCASPutFromPath_PromoteTmpFile(t *testing.T) {
	root := t.TempDir()
	c := storage.NewCAS(root)

	// Write a temp file inside the same filesystem as root so rename is
	// atomic (see action block).
	tmpDir := filepath.Join(root, ".uploads")
	if err := os.MkdirAll(tmpDir, 0o750); err != nil {
		t.Fatal(err)
	}
	srcPath := filepath.Join(tmpDir, "upload-xyz")
	if err := os.WriteFile(srcPath, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	digest, n, err := c.PutFromPath(context.Background(), srcPath)
	if err != nil {
		t.Fatalf("PutFromPath: %v", err)
	}
	if digest != helloDigest {
		t.Fatalf("digest = %q, want %q", digest, helloDigest)
	}
	if n != 5 {
		t.Fatalf("size = %d, want 5", n)
	}
	// Src must no longer exist (consumed by atomic rename or unlinked on
	// idempotent skip).
	if _, err := os.Stat(srcPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("src still exists after PutFromPath: err=%v", err)
	}
	// Final blob path exists with the expected bytes.
	hex := helloDigest[len("sha256:"):]
	final := filepath.Join(root, "sha256", hex[:2], hex)
	got, err := os.ReadFile(final)
	if err != nil {
		t.Fatalf("read final blob: %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("final = %q, want hello", got)
	}
}

// TestCASPutFromPath_Idempotent asserts that calling PutFromPath for content
// already in the CAS returns the same digest and consumes the source file
// without disturbing the existing blob (inode preserved).
func TestCASPutFromPath_Idempotent(t *testing.T) {
	root := t.TempDir()
	c := storage.NewCAS(root)

	// Seed the CAS with "hello" via Put.
	d1, _, err := c.Put(context.Background(), bytes.NewReader([]byte("hello")))
	if err != nil {
		t.Fatal(err)
	}
	hex := d1[len("sha256:"):]
	finalPath := filepath.Join(root, "sha256", hex[:2], hex)
	info1, err := os.Stat(finalPath)
	if err != nil {
		t.Fatal(err)
	}

	// Now a tmp file with the same bytes; PutFromPath must consume it
	// without disturbing the existing blob.
	tmpDir := filepath.Join(root, ".uploads")
	if err := os.MkdirAll(tmpDir, 0o750); err != nil {
		t.Fatal(err)
	}
	srcPath := filepath.Join(tmpDir, "upload-dup")
	if err := os.WriteFile(srcPath, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	d2, n, err := c.PutFromPath(context.Background(), srcPath)
	if err != nil {
		t.Fatalf("PutFromPath idempotent: %v", err)
	}
	if d2 != d1 {
		t.Fatalf("digest differs: %q vs %q", d1, d2)
	}
	if n != 5 {
		t.Fatalf("size = %d, want 5", n)
	}
	if _, err := os.Stat(srcPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("src still exists after idempotent PutFromPath: err=%v", err)
	}
	info2, err := os.Stat(finalPath)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(info1, info2) {
		t.Fatalf("existing blob disturbed by idempotent PutFromPath")
	}
}

// TestCASPutFromPath_MissingSource asserts the error wraps fs.ErrNotExist.
func TestCASPutFromPath_MissingSource(t *testing.T) {
	root := t.TempDir()
	c := storage.NewCAS(root)

	_, _, err := c.PutFromPath(context.Background(), filepath.Join(root, "does-not-exist"))
	if err == nil {
		t.Fatal("expected error on missing source, got nil")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err not fs.ErrNotExist: %v", err)
	}
}

func TestTrimSHA256Prefix(t *testing.T) {
	cases := map[string]string{
		"sha256:abc123": "abc123",
		"abc123":        "abc123",
		"sha256:":       "sha256:", // bare prefix is not a digest — unchanged
		"":              "",
		"sha512:abc":    "sha512:abc",
	}
	for in, want := range cases {
		if got := storage.TrimSHA256Prefix(in); got != want {
			t.Errorf("TrimSHA256Prefix(%q) = %q, want %q", in, got, want)
		}
	}
}
