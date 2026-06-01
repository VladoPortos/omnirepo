package storage_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/vladoportos/omnirepo/internal/storage"
)

func TestTrashMoveRestore(t *testing.T) {
	root := t.TempDir()
	trashRoot := filepath.Join(root, "trash")
	srcDir := filepath.Join(root, "live", "acme", "docker", "oracle")
	if err := os.MkdirAll(srcDir, 0o750); err != nil {
		t.Fatal(err)
	}
	// Seed a file inside
	seed := filepath.Join(srcDir, "a.txt")
	if err := os.WriteFile(seed, []byte("content"), 0o640); err != nil {
		t.Fatal(err)
	}

	tr := storage.NewTrash(trashRoot)
	trashPath, err := tr.Move(context.Background(), srcDir, "repo", 42, "alice")
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

	// Move must persist the original path so Restore can
	// put the tree back at the exact pre-delete location.
	if entries[0].OriginalPath != srcDir {
		t.Fatalf("OriginalPath = %q, want %q", entries[0].OriginalPath, srcDir)
	}
	// Move must persist the actor login so the admin Trash UI can
	// render who triggered the soft-delete.
	if entries[0].DeletedByUser != "alice" {
		t.Fatalf("DeletedByUser = %q, want alice", entries[0].DeletedByUser)
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

// TestTrashMove_SameSecondSameID_NoCollision is a regression test. Per-file
// deletes (RAW/rpm/deb/pypi/helm) pass the shared repo id, so two files removed
// from one repo in the same wall-clock second previously produced an identical
// holder dir: the second Move's MkdirAll no-op'd onto the first's dir, its
// sidecar overwrote the first's, List surfaced only one entry, and (for files
// sharing a basename) the rename overwrote content outright — then GC silently
// destroyed the unrecoverable remainder. Each Move must now get its own holder,
// sidecar, and content.
func TestTrashMove_SameSecondSameID_NoCollision(t *testing.T) {
	root := t.TempDir()
	trashRoot := filepath.Join(root, "trash")
	tr := storage.NewTrash(trashRoot)

	const repoID = 7
	// Two distinct files in the same repo deliberately sharing a basename
	// ("data.bin") in different subdirs — the worst case, which previously
	// caused an immediate content overwrite on rename.
	mk := func(rel, body string) string {
		p := filepath.Join(root, "live", "proj", "raw", "myrepo", rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o640); err != nil {
			t.Fatal(err)
		}
		return p
	}
	srcA := mk("dir1/data.bin", "AAA")
	srcB := mk("dir2/data.bin", "BBB")

	pathA, err := tr.Move(context.Background(), srcA, "raw-file", repoID, "alice")
	if err != nil {
		t.Fatalf("Move A: %v", err)
	}
	pathB, err := tr.Move(context.Background(), srcB, "raw-file", repoID, "bob")
	if err != nil {
		t.Fatalf("Move B: %v", err)
	}
	if filepath.Dir(pathA) == filepath.Dir(pathB) {
		t.Fatalf("holder collision: both files landed in %s", filepath.Dir(pathA))
	}
	if b, _ := os.ReadFile(pathA); string(b) != "AAA" {
		t.Fatalf("file A content = %q, want AAA", b)
	}
	if b, _ := os.ReadFile(pathB); string(b) != "BBB" {
		t.Fatalf("file B content = %q, want BBB", b)
	}

	// List must surface BOTH, each with its own OriginalPath sidecar.
	entries, err := tr.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("List len=%d want 2 (one per file)", len(entries))
	}
	gotPaths := map[string]bool{}
	for _, e := range entries {
		if e.Kind != "raw-file" || e.OriginalID != repoID {
			t.Fatalf("entry = %+v", e)
		}
		gotPaths[e.OriginalPath] = true
	}
	if !gotPaths[srcA] || !gotPaths[srcB] {
		t.Fatalf("OriginalPaths = %v, want both %q and %q", gotPaths, srcA, srcB)
	}

	// Each is independently restorable to its own original location.
	if err := tr.Restore(context.Background(), pathA, srcA); err != nil {
		t.Fatalf("Restore A: %v", err)
	}
	if err := tr.Restore(context.Background(), pathB, srcB); err != nil {
		t.Fatalf("Restore B: %v", err)
	}
	if b, _ := os.ReadFile(srcA); string(b) != "AAA" {
		t.Fatalf("restored A = %q, want AAA", b)
	}
	if b, _ := os.ReadFile(srcB); string(b) != "BBB" {
		t.Fatalf("restored B = %q, want BBB", b)
	}
}

// TestTrashListHandlesMissingSidecar proves backward compat: legacy entries
// written before the sidecar was introduced have no sidecar; List must still
// return them (with OriginalPath empty) so admin trash UI can still show and
// purge them.
func TestTrashListHandlesMissingSidecar(t *testing.T) {
	root := t.TempDir()
	trashRoot := filepath.Join(root, "trash")

	// Simulate a legacy holder without a sidecar.
	holder := filepath.Join(trashRoot, "1700000000-repo-7")
	if err := os.MkdirAll(filepath.Join(holder, "legacy-repo-name"), 0o750); err != nil {
		t.Fatal(err)
	}

	tr := storage.NewTrash(trashRoot)
	entries, err := tr.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("len=%d want 1", len(entries))
	}
	if entries[0].Kind != "repo" || entries[0].OriginalID != 7 {
		t.Fatalf("holder parse wrong: %+v", entries[0])
	}
	if entries[0].OriginalPath != "" {
		t.Fatalf("legacy OriginalPath = %q, want empty", entries[0].OriginalPath)
	}
}

// TestTrash_MoveWithSnapshot_RoundTrip proves the round-trip: a
// MoveWithSnapshot call writes a sidecar carrying the row snapshot
// verbatim, and List surfaces the snapshot bytes byte-for-byte on
// TrashEntry.RowSnapshot.
func TestTrash_MoveWithSnapshot_RoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	trashRoot := filepath.Join(dir, "trash")
	srcDir := filepath.Join(dir, "src")
	if err := os.MkdirAll(srcDir, 0o750); err != nil {
		t.Fatal(err)
	}
	srcFile := filepath.Join(srcDir, "foo-1.0.0.tar.gz")
	if err := os.WriteFile(srcFile, []byte("dummy"), 0o640); err != nil {
		t.Fatal(err)
	}

	tr := storage.NewTrash(trashRoot)
	ctx := context.Background()

	snapshot := json.RawMessage(`{"repo_id":42,"filename":"foo-1.0.0.tar.gz","digest":"sha256:abcd"}`)
	if _, err := tr.MoveWithSnapshot(ctx, srcFile, "pypi_file_drift", 7, "alice", snapshot); err != nil {
		t.Fatalf("MoveWithSnapshot: %v", err)
	}

	entries, err := tr.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1", len(entries))
	}
	e := entries[0]
	if e.Kind != "pypi_file_drift" {
		t.Errorf("Kind = %q, want pypi_file_drift", e.Kind)
	}
	if e.OriginalID != 7 {
		t.Errorf("OriginalID = %d, want 7", e.OriginalID)
	}
	if e.DeletedByUser != "alice" {
		t.Errorf("DeletedByUser = %q, want alice", e.DeletedByUser)
	}
	if string(e.RowSnapshot) != string(snapshot) {
		t.Errorf("RowSnapshot = %s, want %s", e.RowSnapshot, snapshot)
	}
}

// TestTrash_Move_NoSnapshotField proves that plain Move never sets
// RowSnapshot — required so the restore handler can use
// `entry.RowSnapshot != nil` as the dispatch signal between
// generic-trash and drift-restore code paths.
func TestTrash_Move_NoSnapshotField(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	trashRoot := filepath.Join(dir, "trash")
	srcDir := filepath.Join(dir, "src")
	if err := os.MkdirAll(srcDir, 0o750); err != nil {
		t.Fatal(err)
	}
	srcFile := filepath.Join(srcDir, "bar.tar.gz")
	if err := os.WriteFile(srcFile, []byte("d"), 0o640); err != nil {
		t.Fatal(err)
	}

	tr := storage.NewTrash(trashRoot)
	ctx := context.Background()
	if _, err := tr.Move(ctx, srcFile, "repo", 5, "bob"); err != nil {
		t.Fatalf("Move: %v", err)
	}

	entries, err := tr.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1", len(entries))
	}
	if entries[0].RowSnapshot != nil {
		t.Errorf("RowSnapshot = %q, want nil (plain Move must not set snapshot)", entries[0].RowSnapshot)
	}
}

// TestTrash_ListDecodesLegacySidecarWithoutRowSnapshot proves
// forwards-compat: a sidecar written before row snapshotting existed (no
// row_snapshot key) decodes successfully through the new struct
// shape with RowSnapshot==nil and all old fields preserved.
func TestTrash_ListDecodesLegacySidecarWithoutRowSnapshot(t *testing.T) {
	t.Parallel()
	trashRoot := t.TempDir()
	// Simulate a pre-v1.5 sidecar hand-written with the OLD shape
	// (no row_snapshot key). Create the holder dir, write sidecar
	// JSON with only old fields, plus a dummy content file so the
	// holder isn't empty.
	holder := filepath.Join(trashRoot, "1000000-repo-42")
	if err := os.MkdirAll(holder, 0o750); err != nil {
		t.Fatal(err)
	}
	contentPath := filepath.Join(holder, "legacy-file.txt")
	if err := os.WriteFile(contentPath, []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}
	legacyMeta := []byte(`{"original_path":"/var/orig/legacy-file.txt","kind":"repo","original_id":42,"moved_at_unix":1000000,"deleted_by":"admin"}`)
	if err := os.WriteFile(filepath.Join(holder, "omnirepo-trash.json"), legacyMeta, 0o640); err != nil {
		t.Fatal(err)
	}

	tr := storage.NewTrash(trashRoot)
	entries, err := tr.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1", len(entries))
	}
	if entries[0].RowSnapshot != nil {
		t.Errorf("RowSnapshot = %q, want nil (legacy sidecar has no row_snapshot key)", entries[0].RowSnapshot)
	}
	if entries[0].Kind != "repo" {
		t.Errorf("Kind = %q, want repo (legacy fields still decode)", entries[0].Kind)
	}
	if entries[0].OriginalID != 42 {
		t.Errorf("OriginalID = %d, want 42", entries[0].OriginalID)
	}
	if entries[0].OriginalPath != "/var/orig/legacy-file.txt" {
		t.Errorf("OriginalPath = %q, want /var/orig/legacy-file.txt", entries[0].OriginalPath)
	}
	if entries[0].DeletedByUser != "admin" {
		t.Errorf("DeletedByUser = %q, want admin", entries[0].DeletedByUser)
	}
}

// TestTrash_MoveWithSnapshot_NilSnapshot_OmitsKey proves the
// omitempty wire-compat invariant: a MoveWithSnapshot call with a
// nil snapshot writes a sidecar that does NOT contain the
// row_snapshot key, so a downgrade to v1.4 (or any reader that
// rejects unknown keys) keeps working.
func TestTrash_MoveWithSnapshot_NilSnapshot_OmitsKey(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	trashRoot := filepath.Join(dir, "trash")
	srcDir := filepath.Join(dir, "src")
	if err := os.MkdirAll(srcDir, 0o750); err != nil {
		t.Fatal(err)
	}
	srcFile := filepath.Join(srcDir, "x.tar.gz")
	if err := os.WriteFile(srcFile, []byte("d"), 0o640); err != nil {
		t.Fatal(err)
	}

	tr := storage.NewTrash(trashRoot)
	trashPath, err := tr.MoveWithSnapshot(context.Background(), srcFile, "repo", 1, "", nil)
	if err != nil {
		t.Fatalf("MoveWithSnapshot: %v", err)
	}

	// Read the sidecar JSON raw bytes and confirm row_snapshot key is
	// absent (omitempty).
	holderDir := filepath.Dir(trashPath)
	raw, err := os.ReadFile(filepath.Join(holderDir, "omnirepo-trash.json"))
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	if bytes.Contains(raw, []byte("row_snapshot")) {
		t.Errorf("sidecar bytes contain row_snapshot key despite nil snapshot; omitempty broken:\n%s", raw)
	}
}
