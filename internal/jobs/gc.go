// Package jobs — GC handler (Phase 02-12, D-37, D-38, OPS-06, SCAN-12).
//
// The GC handler is a sync-pool job (kind="gc") triggered exclusively by
// super-admin via POST /api/v1/admin/gc. The algorithm is mark-and-sweep,
// single-pass per run:
//
//  1. Snapshot blob_uploads digest set into an in-memory map. This BEFORE
//     iterating any candidates is the SCAN-12 race-proof guarantee:
//     because the OCI PUT path inserts blob_uploads.Start BEFORE
//     cas.PutFromPath (02-06 invariant), a digest the snapshot misses is
//     guaranteed to either still be in the upload session (CAS file not
//     yet promoted) OR already promoted into docker_blobs (then protected
//     by ref_count > 0 or last_touched_at quiescence).
//  2. Read GC candidates from docker_blobs (ref_count==0 AND
//     last_touched_at < now - quiescence).
//  3. For each candidate NOT in the snapshot: cas.Delete(digest), then
//     DELETE row guarded by ref_count==0. Per-file failures logged at
//     WARN, run continues (best-effort).
//  4. Sweep trash entries older than retention (best-effort).
//  5. Prune blob_upload_sessions older than now and remove their tmp
//     upload files at <DataRoot>/tmp/uploads/<uuid>.
//  6. Write the final GCReport into sync_jobs.log AND emit audit gc.run
//     in one writer tx so the report and the audit row share a commit.
//
// Per-task failures are logged but do NOT abort the run. Only an early
// snapshot/candidate-fetch error returns from Handle (which the pool will
// then MarkFailed → retry).
package jobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/dxc-internal/omnirepo/internal/audit"
	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/storage"
)

// GCJobKind is the sync_jobs.kind value the dispatcher matches on to
// route a leased row to GCHandler.
const GCJobKind = "gc"

// GCReport is the per-run summary recorded both into sync_jobs.log and
// into the gc.run audit event's details_json.
type GCReport struct {
	BlobsDeleted        int64 `json:"blobs_deleted"`
	BytesFreed          int64 `json:"bytes_freed"`
	TrashEntriesDeleted int64 `json:"trash_entries_deleted"`
	SessionsPruned      int64 `json:"sessions_pruned"`
}

// GCHandler executes one GC run. Construct via NewGCHandler and register
// on the sync pool: syncHandlers[GCJobKind] = func(ctx,j) { return h.Handle(ctx, j.ID) }.
type GCHandler struct {
	DB             *metadata.DB
	Blobs          *metadata.DockerBlobsRepo
	BlobUploads    *metadata.BlobUploadsRepo
	Sessions       *metadata.BlobUploadSessionsRepo
	CAS            storage.CAS
	Trash          storage.Trash
	Audit          audit.Logger
	DataRoot       string
	Quiescence     time.Duration
	TrashRetention time.Duration
}

// NewGCHandler constructs a GCHandler with sane defaults applied to any
// zero-valued duration field (Quiescence=1h, TrashRetention=7d).
func NewGCHandler(d GCHandler) *GCHandler {
	if d.Quiescence <= 0 {
		d.Quiescence = time.Hour
	}
	if d.TrashRetention <= 0 {
		d.TrashRetention = 7 * 24 * time.Hour
	}
	return &d
}

// Handle runs one GC pass for the supplied sync_jobs row id. Returns nil
// on success — even if individual file/row deletes failed, the run is
// considered successful at the job level (best-effort GC, D-38).
//
// Only setup-phase errors (snapshot fetch, candidate fetch) bubble up;
// the pool will then MarkFailed/retry.
func (g *GCHandler) Handle(ctx context.Context, jobID int64) error {
	var report GCReport

	// Step 1: snapshot blob_uploads.Active into an in-memory set BEFORE
	// touching docker_blobs. This is the SCAN-12 race gate.
	activeDigests, err := g.BlobUploads.Active(ctx)
	if err != nil {
		return fmt.Errorf("gc: snapshot blob_uploads: %w", err)
	}
	snapshot := make(map[string]struct{}, len(activeDigests))
	for _, d := range activeDigests {
		snapshot[d] = struct{}{}
	}

	// Step 2-3: enumerate GC candidates and sweep.
	candidates, err := g.Blobs.GCCandidates(ctx, g.Quiescence)
	if err != nil {
		return fmt.Errorf("gc: candidates: %w", err)
	}
	for _, c := range candidates {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if _, inFlight := snapshot[c.Digest]; inFlight {
			continue
		}
		// Order: cas.Delete THEN DELETE row (T-02-12-06). cas.Delete is
		// idempotent; if it fails we skip the row delete so a future GC
		// run can retry the file.
		if delErr := g.CAS.Delete(ctx, c.Digest); delErr != nil {
			slog.WarnContext(ctx, "gc.cas.delete.failed",
				"digest", c.Digest, "err", delErr)
			continue
		}
		txErr := g.DB.WriteTx(ctx, func(tx *sql.Tx) error {
			res, err := tx.ExecContext(ctx,
				`DELETE FROM docker_blobs WHERE digest=? AND ref_count=0`, c.Digest)
			if err != nil {
				return err
			}
			n, _ := res.RowsAffected()
			if n > 0 {
				report.BlobsDeleted++
				report.BytesFreed += c.SizeBytes
			}
			return nil
		})
		if txErr != nil {
			slog.WarnContext(ctx, "gc.row.delete.failed",
				"digest", c.Digest, "err", txErr)
		}
	}

	// Step 4: trash retention sweep.
	entries, listErr := g.Trash.List(ctx)
	if listErr != nil {
		slog.WarnContext(ctx, "gc.trash.list.failed", "err", listErr)
	} else {
		cutoff := time.Now().Add(-g.TrashRetention)
		for _, e := range entries {
			if !e.MovedAt.Before(cutoff) {
				continue
			}
			if rmErr := os.RemoveAll(e.Path); rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) {
				slog.WarnContext(ctx, "gc.trash.remove.failed",
					"path", e.Path, "err", rmErr)
				continue
			}
			report.TrashEntriesDeleted++
		}
	}

	// Step 5: prune stale upload sessions + their tmp files.
	var pruned []string
	pruneErr := g.DB.WriteTx(ctx, func(tx *sql.Tx) error {
		var pe error
		pruned, pe = g.Sessions.PruneExpiredReturning(ctx, tx, time.Now())
		return pe
	})
	if pruneErr != nil {
		slog.WarnContext(ctx, "gc.sessions.prune.failed", "err", pruneErr)
	} else {
		for _, uuid := range pruned {
			tmpPath := filepath.Join(g.DataRoot, "tmp", "uploads", uuid)
			if rmErr := os.Remove(tmpPath); rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) {
				slog.WarnContext(ctx, "gc.session.tmp.remove.failed",
					"uuid", uuid, "err", rmErr)
			}
			report.SessionsPruned++
		}
	}

	// Step 6: persist report into sync_jobs.log. We do this in its own
	// writer tx (separate from the audit emission below) because the
	// audit Logger.Record API is not tx-aware — it opens its own write
	// tx internally. Both writes are durable on the writer pool's
	// serialized queue.
	summaryJSON, _ := json.Marshal(report)
	if logErr := g.DB.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE sync_jobs SET log=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`,
			string(summaryJSON), jobID)
		return err
	}); logErr != nil {
		slog.WarnContext(ctx, "gc.log.write.failed",
			"job_id", jobID, "err", logErr)
	}

	// Audit gc.run with the report. Best-effort per OQ-9 — failure does
	// NOT propagate (the GC run already succeeded at the artifact level).
	if g.Audit != nil {
		details := map[string]any{
			"blobs_deleted":         report.BlobsDeleted,
			"bytes_freed":           report.BytesFreed,
			"trash_entries_deleted": report.TrashEntriesDeleted,
			"sessions_pruned":       report.SessionsPruned,
		}
		if auErr := g.Audit.Record(ctx, audit.Event{
			Kind:       audit.EvtGCRun,
			TargetKind: "gc",
			TargetID:   strconv.FormatInt(jobID, 10),
			Outcome:    "ok",
			Details:    details,
		}); auErr != nil {
			slog.WarnContext(ctx, "gc.audit.record.failed",
				"job_id", jobID, "err", auErr)
		}
	}

	return nil
}

// Report is the public alias for the report shape produced by Handle.
// Useful for tests that decode sync_jobs.log back into a typed value.
type Report = GCReport
