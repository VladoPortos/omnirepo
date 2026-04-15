package storage_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dxc-internal/omnirepo/internal/storage"
)

func TestPathStorePutGetDeleteExists(t *testing.T) {
	root := t.TempDir()
	ps := storage.NewPathStore(root)

	n, err := ps.Put(context.Background(), "dxc/raw/foo/bar.txt", bytes.NewReader([]byte("content")))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if n != 7 {
		t.Fatalf("n=%d want 7", n)
	}
	// Written at root/dxc/raw/foo/bar.txt
	onDisk := filepath.Join(root, "dxc", "raw", "foo", "bar.txt")
	if _, err := os.Stat(onDisk); err != nil {
		t.Fatalf("stat on-disk: %v", err)
	}

	exists, err := ps.Exists(context.Background(), "dxc/raw/foo/bar.txt")
	if err != nil || !exists {
		t.Fatalf("Exists=%v err=%v", exists, err)
	}
	rc, err := ps.Get(context.Background(), "dxc/raw/foo/bar.txt")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(rc)
	_ = rc.Close()
	if string(got) != "content" {
		t.Fatalf("Get = %q", got)
	}
	if err := ps.Delete(context.Background(), "dxc/raw/foo/bar.txt"); err != nil {
		t.Fatal(err)
	}
	exists, _ = ps.Exists(context.Background(), "dxc/raw/foo/bar.txt")
	if exists {
		t.Fatal("Exists true after Delete")
	}
}

func TestPathStoreRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	ps := storage.NewPathStore(root)

	cases := []string{
		"../../../etc/passwd",
		"foo/../../etc/passwd",
		"/etc/passwd",
		"../escape",
	}
	for _, key := range cases {
		_, err := ps.Put(context.Background(), key, bytes.NewReader([]byte("x")))
		if err == nil {
			t.Errorf("Put(%q): expected traversal error, got nil", key)
			continue
		}
		if !strings.Contains(err.Error(), "invalid") && !strings.Contains(err.Error(), "escape") && !strings.Contains(err.Error(), "path") {
			t.Errorf("Put(%q): err=%v, expected invalid-key style error", key, err)
		}
	}
}
