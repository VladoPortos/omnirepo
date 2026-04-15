package storage_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/dxc-internal/omnirepo/internal/storage"
)

func TestTrashMoveRestore(t *testing.T) {
	root := t.TempDir()
	trashRoot := filepath.Join(root, "trash")
	srcDir := filepath.Join(root, "live", "dxc", "docker", "oracle")
	if err := os.MkdirAll(srcDir, 0o750); err != nil {
		t.Fatal(err)
	}
	// Seed a file inside
	seed := filepath.Join(srcDir, "a.txt")
	if err := os.WriteFile(seed, []byte("content"), 0o640); err != nil {
		t.Fatal(err)
	}

	tr := storage.NewTrash(trashRoot)
	trashPath, err := tr.Move(context.Background(), srcDir, "repo", 42)
	if err != nil {
		t.Fatalf("Move: %v", err)
	}
	// Src gone
	if _, err := os.Stat(srcDir); !os.IsNotExist(err) {
		t.Fatalf("src still exists: err=%v", err)
	}
	// Trash path exists and contains seed
	if _, err := os.Stat(filepath.Join(trashPath, "a.txt")); err != nil {
		t.Fatalf("trash content: %v", err)
	}

	// List
	entries, err := tr.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("List len=%d want 1", len(entries))
	}
	if entries[0].Kind != "repo" || entries[0].OriginalID != 42 {
		t.Fatalf("entry = %+v", entries[0])
	}
	if entries[0].MovedAt.IsZero() {
		t.Fatal("MovedAt zero")
	}

	// Restore
	if err := tr.Restore(context.Background(), trashPath, srcDir); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if _, err := os.Stat(filepath.Join(srcDir, "a.txt")); err != nil {
		t.Fatalf("restored content missing: %v", err)
	}
	entries, _ = tr.List(context.Background())
	if len(entries) != 0 {
		t.Fatalf("List after Restore len=%d want 0", len(entries))
	}
}
