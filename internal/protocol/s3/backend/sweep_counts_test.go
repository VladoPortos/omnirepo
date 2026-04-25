package backend_test

// Plan 02-04 Task 2 — SweepOrphanMultiparts returns counts.
//
// New signature: (ctx, cutoff time.Time) -> (swept, cleanedDirs int, err error).
// The boot-recovery goroutine ignores the returned counts; the new
// admin handler at POST /api/v1/admin/maintenance/sweep-multipart uses
// them. A single function — no parallel WithCounts variant (W-4 fix).

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestSweepOrphanMultiparts_ReturnsCounts seeds 2 stale uploads + 1 fresh
// upload and asserts the new (swept, cleanedDirs, err) signature.
func TestSweepOrphanMultiparts_ReturnsCounts(t *testing.T) {
	f := newFixture(t)
	if err := f.b.CreateBucket("bkt"); err != nil {
		t.Fatal(err)
	}
	// Two stale uploads with on-disk staging dirs.
	staleIDs := make([]string, 0, 2)
	for _, k := range []string{"stale-a", "stale-b"} {
		id, err := fixtureCreateMPU(t, f, "bkt", k, nil)
		if err != nil {
			t.Fatalf("seed stale %s: %v", k, err)
		}
		staleIDs = append(staleIDs, string(id))
		// Force initiated_at into the deep past.
		if _, err := f.db.Writer.ExecContext(context.Background(),
			`UPDATE s3_multipart_uploads SET initiated_at='2020-01-01T00:00:00.000Z' WHERE upload_id=?`,
			string(id),
		); err != nil {
			t.Fatal(err)
		}
	}
	// One fresh upload — must NOT be swept.
	fresh, err := fixtureCreateMPU(t, f, "bkt", "fresh", nil)
	if err != nil {
		t.Fatal(err)
	}

	// Sweep at "now" — cutoff = now - 24h drops the two stale rows.
	cutoff := time.Now().Add(-24 * time.Hour)
	swept, cleanedDirs, err := f.b.SweepOrphanMultiparts(context.Background(), cutoff)
	if err != nil {
		t.Fatalf("SweepOrphanMultiparts: %v", err)
	}
	if swept != 2 {
		t.Errorf("swept = %d, want 2", swept)
	}
	// Each stale upload had a staging dir created by CreateMultipartUploadCtx
	// → AbortMultipartUpload should have RemoveAll'd both of them.
	if cleanedDirs != 2 {
		t.Errorf("cleanedDirs = %d, want 2", cleanedDirs)
	}
	// Verify on-disk staging dirs are gone for stale, present for fresh.
	for _, id := range staleIDs {
		if _, err := readDir(f, id); err == nil {
			t.Errorf("staging dir for stale %q still exists", id)
		}
	}
	if _, err := readDir(f, string(fresh)); err != nil {
		t.Errorf("staging dir for fresh upload missing: %v", err)
	}
}

// TestSweepOrphanMultiparts_NoStaleReturnsZero pins the empty-input branch:
// swept=0, cleaned=0, no error.
func TestSweepOrphanMultiparts_NoStaleReturnsZero(t *testing.T) {
	f := newFixture(t)
	if err := f.b.CreateBucket("bkt"); err != nil {
		t.Fatal(err)
	}
	if _, err := fixtureCreateMPU(t, f, "bkt", "fresh", nil); err != nil {
		t.Fatal(err)
	}
	swept, cleanedDirs, err := f.b.SweepOrphanMultiparts(
		context.Background(), time.Now().Add(-24*time.Hour),
	)
	if err != nil {
		t.Fatalf("SweepOrphanMultiparts: %v", err)
	}
	if swept != 0 {
		t.Errorf("swept = %d, want 0 (fresh upload is in-window)", swept)
	}
	if cleanedDirs != 0 {
		t.Errorf("cleanedDirs = %d, want 0", cleanedDirs)
	}
}

// readDir is a tiny helper that surfaces os.Stat on a multipart staging dir.
func readDir(f *fixture, uploadID string) (os.FileInfo, error) {
	return os.Stat(filepath.Join(f.dataRoot, "tmp", "s3", uploadID))
}
