package storage_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/dxc-internal/omnirepo/internal/storage"
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
