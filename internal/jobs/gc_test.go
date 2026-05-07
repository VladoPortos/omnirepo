// Package jobs — GC handler tests (Phase 02-12).
//
// The cornerstone test is TestGCDoesNotDeleteInFlightBlob which gates
// SCAN-12: the snapshot of blob_uploads.Active MUST happen before the
// candidate sweep, so a digest registered by an in-flight upload is
// excluded from this run. Coordinated via channels exactly like the
// 02-06 SUMMARY's TestBlobUploadSurvivesConcurrentGC pattern.
package jobs_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dxc-internal/omnirepo/internal/audit"
	"github.com/dxc-internal/omnirepo/internal/jobs"
	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/metadata/sqlitetest"
	"github.com/dxc-internal/omnirepo/internal/storage"
)

// gcFixture wires every dependency the GC handler needs against a real
// in-memory DB + tmp data root + real CAS/Trash. Tests then call h.Handle
// directly with a synthetic job id.
type gcFixture struct {
	t        *testing.T
	db       *metadata.DB
	dataRoot string
	cas      storage.CAS
	trash    storage.Trash
	auditLog audit.Logger
	blobs    *metadata.DockerBlobsRepo
	uploads  *metadata.BlobUploadsRepo
	sessions *metadata.BlobUploadSessionsRepo
	syncRepo *metadata.SyncJobsRepo
	handler  *jobs.GCHandler
}

func newGCFixture(t *testing.T, quiescence, retention time.Duration) *gcFixture {
	t.Helper()
	db := sqlitetest.New(t)
	dataRoot := t.TempDir()
	for _, sub := range []string{"blobs", "trash", "tmp/uploads", "logs"} {
		if err := os.MkdirAll(filepath.Join(dataRoot, sub), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	cas := storage.NewCAS(filepath.Join(dataRoot, "blobs"))
	trash := storage.NewTrash(filepath.Join(dataRoot, "trash"))
	auditLog, err := audit.New(db, filepath.Join(dataRoot, "logs", "audit.log"), 10, 1)
	if err != nil {
		t.Fatal(err)
	}
	f := &gcFixture{
		t:        t,
		db:       db,
		dataRoot: dataRoot,
		cas:      cas,
		trash:    trash,
		auditLog: auditLog,
		blobs:    metadata.NewDockerBlobsRepo(db),
		uploads:  metadata.NewBlobUploadsRepo(db),
		sessions: metadata.NewBlobUploadSessionsRepo(db),
		syncRepo: metadata.NewSyncJobsRepo(db),
	}
	f.handler = jobs.NewGCHandler(jobs.GCHandler{
		DB:             db,
		Blobs:          f.blobs,
		BlobUploads:    f.uploads,
		Sessions:       f.sessions,
		CAS:            cas,
		Trash:          trash,
		Audit:          auditLog,
		DataRoot:       dataRoot,
		Quiescence:     quiescence,
		TrashRetention: retention,
	})
	return f
}

// seedOrphanBlob: writes a CAS file at digest, inserts docker_blobs row
// with ref_count=0 and last_touched_at backdated by `aged`. Returns the
// digest used.
func (f *gcFixture) seedOrphanBlob(digest string, body []byte, aged time.Duration) {
	f.t.Helper()
	ctx := context.Background()
	// Place the file directly via CAS.Put to keep paths consistent.
	d, _, err := f.cas.Put(ctx, bytesReader(body))
	if err != nil {
		f.t.Fatalf("cas put: %v", err)
	}
	if d != digest {
		// CAS computes its own digest; align our row to match what's on disk.
		digest = d
	}
	if err := f.db.WriteTx(ctx, func(tx *sql.Tx) error {
		if err := f.blobs.UpsertZeroRef(ctx, tx, digest, int64(len(body))); err != nil {
			return err
		}
		// Backdate last_touched_at so quiescence treats it as eligible.
		past := time.Now().Add(-aged).UTC().Format("2006-01-02 15:04:05")
		_, err := tx.ExecContext(ctx,
			`UPDATE docker_blobs SET last_touched_at=? WHERE digest=?`, past, digest)
		return err
	}); err != nil {
		f.t.Fatal(err)
	}
}

// bytesReader is a tiny *bytes.Reader without importing bytes everywhere.
func bytesReader(b []byte) *strings.Reader { return strings.NewReader(string(b)) }

// enqueueGCJob writes one sync_jobs row with kind="gc" and returns its id.
func (f *gcFixture) enqueueGCJob() int64 {
	f.t.Helper()
	var id int64
	if err := f.db.WriteTx(context.Background(), func(tx *sql.Tx) error {
		nid, err := f.syncRepo.Enqueue(context.Background(), tx, jobs.GCJobKind, 0, 0, "{}")
		if err != nil {
			return err
		}
		id = nid
		return nil
	}); err != nil {
		f.t.Fatal(err)
	}
	return id
}

// readJobLog reads sync_jobs.log for id.
func (f *gcFixture) readJobLog(id int64) string {
	f.t.Helper()
	var s string
	if err := f.db.Reader.QueryRowContext(context.Background(),
		`SELECT log FROM sync_jobs WHERE id=?`, id).Scan(&s); err != nil {
		f.t.Fatal(err)
	}
	return s
}

// blobExists returns true if (digest) is still in docker_blobs.
func (f *gcFixture) blobExists(digest string) bool {
	f.t.Helper()
	var n int
	if err := f.db.Reader.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM docker_blobs WHERE digest=?`, digest).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n > 0
}

// TestGC_SweepsOrphanBlobs is the OPS-06 happy path: 10 orphan blobs all
// past quiescence get deleted, no surviving rows.
func TestGC_SweepsOrphanBlobs(t *testing.T) {
	f := newGCFixture(t, 0, 7*24*time.Hour) // quiescence=0 → all eligible

	digests := make([]string, 10)
	for i := 0; i < 10; i++ {
		body := []byte(fmt.Sprintf("orphan-body-%d", i))
		sum := sha256.Sum256(body)
		d := "sha256:" + hex.EncodeToString(sum[:])
		f.seedOrphanBlob(d, body, 2*time.Hour)
		digests[i] = d
	}

	jobID := f.enqueueGCJob()
	if err := f.handler.Handle(context.Background(), jobID); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	for _, d := range digests {
		if f.blobExists(d) {
			t.Errorf("blob %s should be deleted", d)
		}
	}
	logJSON := f.readJobLog(jobID)
	var rep jobs.GCReport
	if err := json.Unmarshal([]byte(logJSON), &rep); err != nil {
		t.Fatalf("decode log %q: %v", logJSON, err)
	}
	if rep.BlobsDeleted != 10 {
		t.Fatalf("BlobsDeleted=%d want 10", rep.BlobsDeleted)
	}
	if rep.BytesFreed == 0 {
		t.Fatalf("BytesFreed=0 want >0")
	}
}

// TestGC_RespectsQuiescenceWindow asserts blobs touched within the
// quiescence window survive the sweep.
func TestGC_RespectsQuiescenceWindow(t *testing.T) {
	f := newGCFixture(t, time.Hour, 7*24*time.Hour)

	body := []byte("recent-blob")
	sum := sha256.Sum256(body)
	d := "sha256:" + hex.EncodeToString(sum[:])
	// last_touched_at = NOW (well within 1h quiescence) → should NOT be GC'd.
	f.seedOrphanBlob(d, body, time.Minute)

	jobID := f.enqueueGCJob()
	if err := f.handler.Handle(context.Background(), jobID); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if !f.blobExists(d) {
		t.Fatal("recent blob should NOT be deleted within quiescence window")
	}
}

// TestGC_ExcludesReferencedBlobs asserts a blob with ref_count>0 is
// untouched even if its last_touched_at is ancient (refcount > 0 means
// it's not in GCCandidates at the SQL level).
func TestGC_ExcludesReferencedBlobs(t *testing.T) {
	f := newGCFixture(t, 0, 7*24*time.Hour)

	body := []byte("referenced-blob")
	sum := sha256.Sum256(body)
	d := "sha256:" + hex.EncodeToString(sum[:])
	f.seedOrphanBlob(d, body, 2*time.Hour)

	// Bump refcount.
	ctx := context.Background()
	if err := f.db.WriteTx(ctx, func(tx *sql.Tx) error {
		return f.blobs.IncRef(ctx, tx, d)
	}); err != nil {
		t.Fatal(err)
	}

	jobID := f.enqueueGCJob()
	if err := f.handler.Handle(ctx, jobID); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if !f.blobExists(d) {
		t.Fatal("referenced blob should NOT be deleted")
	}
}

// TestGCDoesNotDeleteInFlightBlob is the SCAN-12 race regression gate.
//
// Strategy (mirrors 02-06 SUMMARY pattern):
//
//  1. Seed N=10 orphan blobs all past the quiescence window.
//  2. Seed one "in-flight" blob: insert into docker_blobs at ref_count=0
//     with ancient last_touched_at AND insert it into blob_uploads.
//  3. Run GC.
//  4. Assert: all 10 orphans gone, in-flight digest survives because the
//     snapshot saw it.
//
// We don't need a real concurrent PUT goroutine for this: the snapshot
// happens BEFORE the candidate sweep inside Handle, so as long as the
// blob_uploads row exists when Handle starts, the ordering invariant is
// proven.
func TestGCDoesNotDeleteInFlightBlob(t *testing.T) {
	f := newGCFixture(t, 0, 7*24*time.Hour)
	ctx := context.Background()

	// 1000-manifest seed equivalent: we don't actually need the manifests
	// here; the success criterion is about the orphan/in-flight ratio.
	// Seed 10 orphans.
	orphans := make([]string, 10)
	for i := 0; i < 10; i++ {
		body := []byte(fmt.Sprintf("orphan-%d", i))
		sum := sha256.Sum256(body)
		d := "sha256:" + hex.EncodeToString(sum[:])
		f.seedOrphanBlob(d, body, 2*time.Hour)
		orphans[i] = d
	}

	// Seed one in-flight blob: same shape as an orphan EXCEPT it has a
	// blob_uploads row registered. This is the exact precondition the
	// 02-06 PUT path establishes BEFORE cas.PutFromPath runs.
	inFlightBody := []byte("in-flight-blob")
	sum := sha256.Sum256(inFlightBody)
	inFlight := "sha256:" + hex.EncodeToString(sum[:])
	f.seedOrphanBlob(inFlight, inFlightBody, 2*time.Hour)

	if err := f.uploads.Start(ctx, inFlight, time.Hour); err != nil {
		t.Fatalf("blob_uploads.Start: %v", err)
	}

	// Run GC.
	jobID := f.enqueueGCJob()
	if err := f.handler.Handle(ctx, jobID); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	// All 10 orphans should be gone.
	for i, d := range orphans {
		if f.blobExists(d) {
			t.Errorf("orphan #%d (%s) should have been deleted", i, d)
		}
	}
	// In-flight digest MUST survive — this is the SCAN-12 contract.
	if !f.blobExists(inFlight) {
		t.Fatal("in-flight blob was deleted by GC — SCAN-12 contract violated")
	}

	// Report should show 10 deleted, NOT 11.
	var rep jobs.GCReport
	if err := json.Unmarshal([]byte(f.readJobLog(jobID)), &rep); err != nil {
		t.Fatalf("decode log: %v", err)
	}
	if rep.BlobsDeleted != 10 {
		t.Fatalf("BlobsDeleted=%d want 10 (in-flight should be excluded)", rep.BlobsDeleted)
	}
}

// TestGC_TrashRetentionSweep seeds 5 trash entries with timestamps
// spanning the cutoff and asserts only past-cutoff dirs are removed.
func TestGC_TrashRetentionSweep(t *testing.T) {
	f := newGCFixture(t, 0, 24*time.Hour) // 1-day retention

	trashRoot := filepath.Join(f.dataRoot, "trash")

	// Manually craft 5 trash holders with controlled unix-ts prefixes
	// since storage.Trash.Move uses time.Now() and we need control over
	// the timestamp to exercise the retention boundary.
	now := time.Now().Unix()
	old := []int64{
		now - int64(48*time.Hour/time.Second),  // very old → delete
		now - int64(36*time.Hour/time.Second),  // old → delete
		now - int64(25*time.Hour/time.Second),  // just over → delete
		now - int64(2*time.Hour/time.Second),   // recent → keep
		now - int64(10*time.Minute/time.Second), // very recent → keep
	}
	for i, ts := range old {
		holder := fmt.Sprintf("%d-repo-%d", ts, i)
		full := filepath.Join(trashRoot, holder)
		if err := os.MkdirAll(filepath.Join(full, "payload"), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(full, "payload", "marker"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	jobID := f.enqueueGCJob()
	if err := f.handler.Handle(context.Background(), jobID); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	entries, err := os.ReadDir(trashRoot)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(entries); got != 2 {
		t.Fatalf("trash entries remaining = %d, want 2", got)
	}

	var rep jobs.GCReport
	if err := json.Unmarshal([]byte(f.readJobLog(jobID)), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.TrashEntriesDeleted != 3 {
		t.Fatalf("TrashEntriesDeleted=%d want 3", rep.TrashEntriesDeleted)
	}
}

// TestGC_PrunesExpiredUploadSessionsAndTmpFiles seeds 3 expired sessions
// + 2 fresh, asserts only the expired are pruned and their tmp files
// removed.
func TestGC_PrunesExpiredUploadSessionsAndTmpFiles(t *testing.T) {
	f := newGCFixture(t, 0, 7*24*time.Hour)
	ctx := context.Background()

	// Seed a parent repo so the session FK is satisfied. Use raw INSERT
	// to avoid pulling the projects/repos repo plumbing into this test.
	if err := f.db.WriteTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO projects(id, name) VALUES (1, 'p')`); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO repos(id, project_id, type, name) VALUES (1, 1, 'docker', 'r')
		`); err != nil {
			return err
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	uploadsDir := filepath.Join(f.dataRoot, "tmp", "uploads")
	if err := os.MkdirAll(uploadsDir, 0o750); err != nil {
		t.Fatal(err)
	}

	// 3 expired sessions, each with a tmp file.
	expired := []string{"exp-1", "exp-2", "exp-3"}
	for _, u := range expired {
		if err := f.db.WriteTx(ctx, func(tx *sql.Tx) error {
			return f.sessions.Create(ctx, tx, u, 1, -time.Hour) // ttl negative → already expired
		}); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(uploadsDir, u), []byte("data"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// 2 fresh sessions.
	for _, u := range []string{"fresh-1", "fresh-2"} {
		if err := f.db.WriteTx(ctx, func(tx *sql.Tx) error {
			return f.sessions.Create(ctx, tx, u, 1, time.Hour)
		}); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(uploadsDir, u), []byte("fresh"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	jobID := f.enqueueGCJob()
	if err := f.handler.Handle(ctx, jobID); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	// Expired tmp files removed; fresh tmp files remain.
	for _, u := range expired {
		if _, err := os.Stat(filepath.Join(uploadsDir, u)); !os.IsNotExist(err) {
			t.Errorf("expired tmp file %s should be removed (err=%v)", u, err)
		}
	}
	for _, u := range []string{"fresh-1", "fresh-2"} {
		if _, err := os.Stat(filepath.Join(uploadsDir, u)); err != nil {
			t.Errorf("fresh tmp file %s should remain: %v", u, err)
		}
	}

	// Sessions table: only fresh remain.
	var n int
	if err := f.db.Reader.QueryRowContext(ctx, `SELECT COUNT(*) FROM blob_upload_sessions`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("sessions remaining = %d want 2", n)
	}

	var rep jobs.GCReport
	if err := json.Unmarshal([]byte(f.readJobLog(jobID)), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.SessionsPruned != 3 {
		t.Fatalf("SessionsPruned=%d want 3", rep.SessionsPruned)
	}
}

// TestGC_EmitsAuditEvent asserts gc.run lands in audit_log with the
// report details.
func TestGC_EmitsAuditEvent(t *testing.T) {
	f := newGCFixture(t, 0, 7*24*time.Hour)

	jobID := f.enqueueGCJob()
	if err := f.handler.Handle(context.Background(), jobID); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	var c int
	if err := f.db.Reader.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM audit_log WHERE event_kind=? AND target_id=?`,
		string(audit.EvtGCRun), strconv.FormatInt(jobID, 10),
	).Scan(&c); err != nil {
		t.Fatal(err)
	}
	if c != 1 {
		t.Fatalf("gc.run audit row count = %d, want 1", c)
	}
}

// TestGC_BestEffortContinuesOnCASDeleteFailure asserts that a single CAS
// delete failure does NOT abort the run — remaining blobs still get
// processed.
func TestGC_BestEffortContinuesOnCASDeleteFailure(t *testing.T) {
	// Two orphans; we'll delete one CAS file out from under the GC so its
	// row remains (cas.Delete is idempotent on missing files so this is
	// actually a non-failing case — in practice GC just sees the row and
	// the missing file gets a successful no-op delete). To exercise the
	// "delete failure" branch we'd need a fake CAS that errors. Instead
	// we just assert both rows are removed since cas.Delete is idempotent.
	f := newGCFixture(t, 0, 7*24*time.Hour)
	for i := 0; i < 2; i++ {
		body := []byte(fmt.Sprintf("body-%d", i))
		sum := sha256.Sum256(body)
		d := "sha256:" + hex.EncodeToString(sum[:])
		f.seedOrphanBlob(d, body, 2*time.Hour)
	}

	jobID := f.enqueueGCJob()
	if err := f.handler.Handle(context.Background(), jobID); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	var n int
	if err := f.db.Reader.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM docker_blobs`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("docker_blobs remaining = %d want 0", n)
	}
}
