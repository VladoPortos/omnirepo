// Package jobs — scaled GC regression.
//
// Seeds ~1000 docker_blobs rows plus ~10 orphan blobs, then runs a
// concurrent GC-loop + upload-loop for 30s and asserts:
//
//  1. Every orphan-at-seed-time was deleted (CAS file + row gone).
//  2. No live blob (ref_count > 0) was deleted.
//  3. No in-flight blob-upload digest was deleted.
//  4. Under `-race`, no data race reported.
//
// The test exercises the exclusion-set contract at scale — the smaller
// `TestGCDoesNotDeleteInFlightBlob` proves correctness with one orphan
// + one in-flight row; this variant proves the same contract holds
// across 1000+ rows under concurrent churn.
package jobs_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestGCScaled_1000Manifests_NoRegressions is the scaled
// regression. Runs for 30s under concurrent GC + new-upload loops.
func TestGCScaled_1000Manifests_NoRegressions(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode skips the 30s scaled regression")
	}

	f := newGCFixture(t, 0 /*quiescence*/, 7*24*time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Seed 1000 "live" blobs (ref_count=1) — these must NEVER be
	// deleted by GC because ref_count > 0.
	liveDigests := make([]string, 0, 1000)
	for i := 0; i < 1000; i++ {
		body := []byte(fmt.Sprintf("live-blob-%d", i))
		sum := sha256.Sum256(body)
		d := "sha256:" + hex.EncodeToString(sum[:])
		f.seedOrphanBlob(d, body, 2*time.Hour)
		// Bump ref_count to 1 so GCCandidates excludes it.
		if err := f.db.WriteTx(ctx, func(tx *sql.Tx) error {
			return f.blobs.IncRef(ctx, tx, d)
		}); err != nil {
			t.Fatalf("incref: %v", err)
		}
		liveDigests = append(liveDigests, d)
	}

	// Seed 10 orphans — ref_count=0, eligible.
	orphans := make([]string, 0, 10)
	for i := 0; i < 10; i++ {
		body := []byte(fmt.Sprintf("orphan-%d-body", i))
		sum := sha256.Sum256(body)
		d := "sha256:" + hex.EncodeToString(sum[:])
		f.seedOrphanBlob(d, body, 2*time.Hour)
		orphans = append(orphans, d)
	}

	// Upload-loop goroutine: during the run, continuously register
	// and un-register blob_uploads entries (Start + the row stays
	// active for 1h TTL). GC's snapshot must catch any that are
	// active at snapshot time.
	//
	// We also insert fresh docker_blobs rows at ref_count=0 with
	// last_touched_at recent so they appear as GC candidates AND
	// simultaneously show up in blob_uploads — the in-flight-upload race.
	var uploadCount atomic.Int64
	var uploadWG sync.WaitGroup
	uploadWG.Add(1)
	go func() {
		defer uploadWG.Done()
		for i := 0; ; i++ {
			if ctx.Err() != nil {
				return
			}
			body := []byte(fmt.Sprintf("inflight-%d-body", i))
			sum := sha256.Sum256(body)
			d := "sha256:" + hex.EncodeToString(sum[:])
			// Order must match the OCI PUT path: blob_uploads.Start
			// BEFORE the docker_blobs row insertion.
			if err := f.uploads.Start(ctx, d, time.Hour); err != nil {
				if ctx.Err() != nil {
					return
				}
				t.Logf("upload.Start: %v", err)
				continue
			}
			_ = f.db.WriteTx(ctx, func(tx *sql.Tx) error {
				return f.blobs.UpsertZeroRef(ctx, tx, d, int64(len(body)))
			})
			uploadCount.Add(1)
			time.Sleep(2 * time.Millisecond)
		}
	}()

	// GC-loop goroutine: run GC every ~200ms for 30s.
	var gcCount atomic.Int64
	var gcWG sync.WaitGroup
	gcWG.Add(1)
	go func() {
		defer gcWG.Done()
		for {
			if ctx.Err() != nil {
				return
			}
			jobID := f.enqueueGCJob()
			if err := f.handler.Handle(ctx, jobID); err != nil {
				if ctx.Err() != nil {
					return
				}
				t.Errorf("GC handle error: %v", err)
				return
			}
			gcCount.Add(1)
			select {
			case <-time.After(200 * time.Millisecond):
			case <-ctx.Done():
				return
			}
		}
	}()

	// Let the loops churn for 30s.
	time.Sleep(30 * time.Second)
	cancel()
	uploadWG.Wait()
	gcWG.Wait()

	t.Logf("scaled run: gc_cycles=%d uploads=%d", gcCount.Load(), uploadCount.Load())

	// Assertion 1: every seeded orphan must be gone (deleted at some
	// point across the 30s run; GC fires multiple times).
	for _, d := range orphans {
		if f.blobExists(d) {
			t.Errorf("orphan %s should have been deleted", d)
		}
	}

	// Assertion 2: every live blob must still exist — ref_count>0 is
	// the protection, and no GC run should have broken it.
	for _, d := range liveDigests {
		if !f.blobExists(d) {
			t.Errorf("live blob %s was deleted (ref_count>0)", d)
		}
	}

	// Assertion 3: every in-flight upload that's still in
	// blob_uploads.Active MUST still have its docker_blobs row.
	active, err := f.uploads.Active(context.Background())
	if err != nil {
		t.Fatalf("uploads.Active: %v", err)
	}
	surviving := 0
	for _, d := range active {
		if !f.blobExists(d) {
			t.Errorf("in-flight blob %s was deleted by GC (in-flight exclusion violated)", d)
		} else {
			surviving++
		}
	}
	t.Logf("active blob_uploads at end=%d all surviving=%d", len(active), surviving)
}
