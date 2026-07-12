package storage_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/vladoportos/omnirepo/internal/storage"
)

func TestPathStorePutGetDeleteExists(t *testing.T) {
	root := t.TempDir()
	ps := storage.NewPathStore(root)

	n, err := ps.Put(context.Background(), "acme/raw/foo/bar.txt", bytes.NewReader([]byte("content")))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if n != 7 {
		t.Fatalf("n=%d want 7", n)
	}
	// Written at root/acme/raw/foo/bar.txt
	onDisk := filepath.Join(root, "acme", "raw", "foo", "bar.txt")
	if _, err := os.Stat(onDisk); err != nil {
		t.Fatalf("stat on-disk: %v", err)
	}

	exists, err := ps.Exists(context.Background(), "acme/raw/foo/bar.txt")
	if err != nil || !exists {
		t.Fatalf("Exists=%v err=%v", exists, err)
	}
	rc, err := ps.Get(context.Background(), "acme/raw/foo/bar.txt")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(rc)
	_ = rc.Close()
	if string(got) != "content" {
		t.Fatalf("Get = %q", got)
	}
	if err := ps.Delete(context.Background(), "acme/raw/foo/bar.txt"); err != nil {
		t.Fatal(err)
	}
	exists, _ = ps.Exists(context.Background(), "acme/raw/foo/bar.txt")
	if exists {
		t.Fatal("Exists true after Delete")
	}
}

func TestPathStoreReplaceRestoresPreviousFileWhenCommitFails(t *testing.T) {
	root := t.TempDir()
	ps := storage.NewPathStore(root)
	ctx := context.Background()
	if err := os.MkdirAll(filepath.Join(root, "repo"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "repo", "file"), []byte("old"), 0o640); err != nil {
		t.Fatal(err)
	}
	commitErr := errors.New("metadata commit failed")
	_, err := ps.Replace(ctx, "repo/file", strings.NewReader("new"), func(int64) error {
		return commitErr
	})
	if !errors.Is(err, commitErr) {
		t.Fatalf("Replace error=%v want %v", err, commitErr)
	}
	rc, err := ps.Get(ctx, "repo/file")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(rc)
	_ = rc.Close()
	if string(got) != "old" {
		t.Fatalf("content=%q want restored old bytes", got)
	}
}

func TestPathStoreReplaceSerializesFileAndCommitForSameKey(t *testing.T) {
	ps := storage.NewPathStore(t.TempDir())
	ctx := context.Background()
	var mu sync.Mutex
	lastCommitted := ""
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, body := range []string{"first", "second"} {
		body := body
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if _, err := ps.Replace(ctx, "repo/file", strings.NewReader(body), func(int64) error {
				mu.Lock()
				lastCommitted = body
				mu.Unlock()
				return nil
			}); err != nil {
				t.Errorf("Replace(%q): %v", body, err)
			}
		}()
	}
	close(start)
	wg.Wait()

	rc, err := ps.Get(ctx, "repo/file")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(rc)
	_ = rc.Close()
	mu.Lock()
	want := lastCommitted
	mu.Unlock()
	if string(got) != want {
		t.Fatalf("file=%q metadata winner=%q", got, want)
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
