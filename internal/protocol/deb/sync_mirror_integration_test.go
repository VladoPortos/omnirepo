package deb_test

// Phase 8 Plan 06 / M6.1 — APT fake-upstream integration test.
//
// Complements sync_progress_test.go (byte-level progress assertion) by
// proving the end-to-end mirror flow:
//   1. First sync ingests N .deb packages from a fake APT upstream
//   2. Progress row reaches a final state (status=running → done sentinel;
//      progress_bytes > 0; progress_bytes == total_bytes; current_step is
//      either "done" or a "pulling <pkg>_<ver>" step)
//   3. Second sync is idempotent: deb_packages row count is unchanged
//
// Uses real metadata.NewReposRepo / metadata.NewSyncJobsRepo /
// metadata.NewDEBPackagesRepo — no shortcut harness helpers. The fake
// upstream is httptest.NewServer serving a valid
// dists/<suite>/{Release, main/binary-amd64/Packages.gz, pool/...} layout.
// The shared fixture uses suite=stable; in the wild, mirror operators
// point at distros like dists/focal for Ubuntu 20.04 main, etc.
// Packages.gz carries accurate Size + SHA256 for two minimal .deb blobs
// built by the sync_progress_test.go shared helper makeMiniDeb.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/dxc-internal/omnirepo/internal/metadata"
)

func TestMirrorSync_APT_Idempotent(t *testing.T) {
	h, db, repoID, upURL := newDEBProgressFixture(t)
	ctx := context.Background()

	// First sync.
	jobID := seedDEBSyncJob(t, db, repoID)
	payload := map[string]any{
		"upstream_url": upURL,
		"suite":        "stable",
		"filter": map[string]any{
			"components": []string{"main"},
			"arches":     []string{"amd64"},
		},
	}
	pb, _ := json.Marshal(payload)
	if err := h.Handle(ctx, string(pb), 0, repoID, jobID); err != nil {
		t.Fatalf("first Handle: %v", err)
	}

	// Count rows after first sync via the real DEBPackagesRepo interface.
	debPkgs := metadata.NewDEBPackagesRepo(db)
	rowsAfterFirst, err := debPkgs.ListByRepo(ctx, repoID)
	if err != nil {
		t.Fatalf("list after first: %v", err)
	}
	if len(rowsAfterFirst) != 2 {
		t.Fatalf("first sync package count = %d; want 2 (curl + bash)", len(rowsAfterFirst))
	}

	// Progress final state on the sync_jobs row.
	var (
		progressBytes, totalBytes int64
		currentStep               string
	)
	if err := db.Reader.QueryRowContext(ctx,
		`SELECT progress_bytes, total_bytes, current_step FROM sync_jobs WHERE id=?`, jobID,
	).Scan(&progressBytes, &totalBytes, &currentStep); err != nil {
		t.Fatalf("scan progress: %v", err)
	}
	if totalBytes <= 0 {
		t.Errorf("total_bytes=%d; want >0 (summed Packages Size:)", totalBytes)
	}
	if progressBytes <= 0 {
		t.Errorf("progress_bytes=%d; want >0", progressBytes)
	}
	if progressBytes != totalBytes {
		t.Errorf("progress_bytes=%d != total_bytes=%d; want equal at end of sync",
			progressBytes, totalBytes)
	}
	if currentStep == "" {
		t.Errorf("current_step=%q; want non-empty (either 'done' or 'pulling <x>_<y>')", currentStep)
	}

	// Second sync — idempotency gate.
	jobID2 := seedDEBSyncJob(t, db, repoID)
	if err := h.Handle(ctx, string(pb), 0, repoID, jobID2); err != nil {
		t.Fatalf("second Handle: %v", err)
	}
	rowsAfterSecond, err := debPkgs.ListByRepo(ctx, repoID)
	if err != nil {
		t.Fatalf("list after second: %v", err)
	}
	if len(rowsAfterSecond) != len(rowsAfterFirst) {
		t.Fatalf("idempotency violated: first=%d, second=%d", len(rowsAfterFirst), len(rowsAfterSecond))
	}

	// On the second run, every entry is filtered out by the digest check in
	// collectFn (FindByDigest hits) so totalBytes should be zero. The
	// terminal "done" emit still lands, so progress_bytes is 0 == total.
	var totalBytes2 int64
	if err := db.Reader.QueryRowContext(ctx,
		`SELECT total_bytes FROM sync_jobs WHERE id=?`, jobID2,
	).Scan(&totalBytes2); err != nil {
		t.Fatalf("scan progress (second): %v", err)
	}
	if totalBytes2 != 0 {
		t.Errorf("second-sync total_bytes=%d; want 0 (all entries already present, nothing to download)", totalBytes2)
	}
}
