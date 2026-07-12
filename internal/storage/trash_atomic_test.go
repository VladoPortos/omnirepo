package storage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestTrashMoveSidecarFailureLeavesSourceLive(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "repos", "acme", "raw", "files", "artifact.bin")
	if err := os.MkdirAll(filepath.Dir(src), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte("payload"), 0o640); err != nil {
		t.Fatal(err)
	}

	previous := writeTrashSidecar
	writeTrashSidecar = func(string, []byte, os.FileMode) error {
		return errors.New("disk full")
	}
	t.Cleanup(func() { writeTrashSidecar = previous })

	trash := NewTrash(filepath.Join(root, "trash"))
	if _, err := trash.Move(context.Background(), src, "raw-file", 1, "alice"); err == nil {
		t.Fatal("Move succeeded despite sidecar failure")
	}
	got, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("source was removed after sidecar failure: %v", err)
	}
	if string(got) != "payload" {
		t.Fatalf("source content=%q", got)
	}
}
