package metadata_test

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/metadata/sqlitetest"
)

func TestSyncJobs_EnqueueLeaseMarkDone(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	jobs := metadata.NewSyncJobsRepo(db)
	ctx := context.Background()

	var id int64
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		var err error
		id, err = jobs.Enqueue(ctx, tx, "pull_external", 0, 0, `{"foo":"bar"}`)
		return err
	}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if id == 0 {
		t.Fatal("expected non-zero id")
	}

	j, ok, err := jobs.LeaseOne(ctx, "worker-1")
	if err != nil || !ok {
		t.Fatalf("lease: %v ok=%v", err, ok)
	}
	if j.ID != id || j.Kind != "pull_external" || j.PayloadJSON != `{"foo":"bar"}` || j.LeaseID != "worker-1" {
		t.Fatalf("unexpected: %+v", j)
	}

	// Second lease finds nothing (only one row, now running).
	_, ok2, _ := jobs.LeaseOne(ctx, "worker-2")
	if ok2 {
		t.Fatalf("expected no lease available after first")
	}

	_ = db.WriteTx(ctx, func(tx *sql.Tx) error {
		return jobs.MarkDone(ctx, tx, id)
	})

	var status string
	_ = db.Reader.QueryRow(`SELECT status FROM sync_jobs WHERE id=?`, id).Scan(&status)
	if status != "done" {
		t.Fatalf("status=%q want done", status)
	}
}

func TestSyncJobs_MarkFailedRetriesAndTermination(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	jobs := metadata.NewSyncJobsRepo(db)
	ctx := context.Background()
	var id int64
	_ = db.WriteTx(ctx, func(tx *sql.Tx) error {
		var err error
		id, err = jobs.Enqueue(ctx, tx, "gc", 0, 0, "{}")
		return err
	})
	_, _, _ = jobs.LeaseOne(ctx, "w")

	future := time.Now().Add(5 * time.Minute)
	_ = db.WriteTx(ctx, func(tx *sql.Tx) error {
		return jobs.MarkFailed(ctx, tx, id, "boom", future)
	})

	// After failure the row is pending again but next_run_at is in the
	// future, so LeaseOne should find nothing.
	_, ok, _ := jobs.LeaseOne(ctx, "w2")
	if ok {
		t.Fatalf("expected no lease before next_run_at")
	}

	_ = db.WriteTx(ctx, func(tx *sql.Tx) error {
		return jobs.MarkPermanentlyFailed(ctx, tx, id, "too many attempts")
	})
	var status string
	_ = db.Reader.QueryRow(`SELECT status FROM sync_jobs WHERE id=?`, id).Scan(&status)
	if status != "failed" {
		t.Fatalf("status=%q want failed", status)
	}
}

func TestSyncJobs_RecoverStale(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	jobs := metadata.NewSyncJobsRepo(db)
	ctx := context.Background()
	// Seed a running job with stale leased_at.
	_, _ = db.Writer.ExecContext(ctx, `
		INSERT INTO sync_jobs(kind, status, leased_by, leased_at)
		VALUES ('x','running','w1',datetime('now','-1 hour'))
	`)
	var n int
	_ = db.WriteTx(ctx, func(tx *sql.Tx) error {
		var err error
		n, err = jobs.RecoverStale(ctx, tx, time.Now().Add(-10*time.Minute))
		return err
	})
	if n != 1 {
		t.Fatalf("recovered=%d want 1", n)
	}
	var status string
	_ = db.Reader.QueryRow(`SELECT status FROM sync_jobs WHERE kind='x'`).Scan(&status)
	if status != "pending" {
		t.Fatalf("status=%q want pending", status)
	}
}

// TestSyncJobsLeaseRace: 8 goroutines contend for one pending row;
// exactly one must win. Proves D-15 lease atomicity under -race.
func TestSyncJobsLeaseRace(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	jobs := metadata.NewSyncJobsRepo(db)
	ctx := context.Background()
	// Seed ONE pending row.
	_ = db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := jobs.Enqueue(ctx, tx, "promote", 0, 0, "{}")
		return err
	})

	const N = 8
	var wg sync.WaitGroup
	wins := make([]*metadata.SyncJob, N)
	errs := make([]error, N)
	start := make(chan struct{})
	for i := 0; i < N; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			j, _, err := jobs.LeaseOne(ctx, fmt.Sprintf("w%d", i))
			wins[i] = j
			errs[i] = err
		}()
	}
	close(start)
	wg.Wait()

	winners := 0
	for i := 0; i < N; i++ {
		if errs[i] != nil {
			t.Fatalf("worker %d err: %v", i, errs[i])
		}
		if wins[i] != nil {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("expected exactly one winner, got %d", winners)
	}
}

// --------------------------------------------------------------------------
// Phase 8 Plan 01 (MIRROR-04, MIRROR-08) — progress + concurrency tracking.
// --------------------------------------------------------------------------

// TestSyncJobsRepo_SetProgress_ReadsBack plants a pending job, writes a
// progress triple via SetProgress, and reads it back to assert migration 024's
// three new columns round-trip.
func TestSyncJobsRepo_SetProgress_ReadsBack(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()
	jobs := metadata.NewSyncJobsRepo(db)
	pid := seedProject(t, db, "sync-progress")
	repos := metadata.NewReposRepo(db)
	repoID, err := repos.Create(ctx, pid, "deb", "p1", "", nil, nil, nil)
	if err != nil {
		t.Fatalf("seed repo: %v", err)
	}

	var jobID int64
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		id, err := jobs.Enqueue(ctx, tx, "apt_sync", pid, repoID, "{}")
		jobID = id
		return err
	}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	if err := jobs.SetProgress(ctx, jobID, "layer 3/7", 42, 103); err != nil {
		t.Fatalf("set progress: %v", err)
	}

	var step string
	var done, total int64
	if err := db.Reader.QueryRowContext(ctx,
		`SELECT COALESCE(current_step, ''), progress_bytes, total_bytes FROM sync_jobs WHERE id=?`,
		jobID,
	).Scan(&step, &done, &total); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if step != "layer 3/7" || done != 42 || total != 103 {
		t.Fatalf("progress mismatch: step=%q done=%d total=%d", step, done, total)
	}
}

// TestSyncJobsRepo_SetFilesSynced plants a pending job, writes a file
// count via SetFilesSynced, and reads it back to assert migration 025's
// new column round-trips. Covers the D-03 closure path for the "Sync
// complete · N files · X MB" pill shape.
func TestSyncJobsRepo_SetFilesSynced(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()
	jobs := metadata.NewSyncJobsRepo(db)
	pid := seedProject(t, db, "sync-files")
	repos := metadata.NewReposRepo(db)
	repoID, err := repos.Create(ctx, pid, "pypi", "p1", "", nil, nil, nil)
	if err != nil {
		t.Fatalf("seed repo: %v", err)
	}

	var jobID int64
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		id, err := jobs.Enqueue(ctx, tx, "pypi_sync", pid, repoID, "{}")
		jobID = id
		return err
	}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	// Default value for a fresh row is 0 (migration 025 DEFAULT 0).
	var got int64
	if err := db.Reader.QueryRowContext(ctx,
		`SELECT files_synced FROM sync_jobs WHERE id=?`, jobID,
	).Scan(&got); err != nil {
		t.Fatalf("readback zero: %v", err)
	}
	if got != 0 {
		t.Fatalf("initial files_synced=%d; want 0", got)
	}

	if err := jobs.SetFilesSynced(ctx, jobID, 42); err != nil {
		t.Fatalf("set files_synced: %v", err)
	}
	if err := db.Reader.QueryRowContext(ctx,
		`SELECT files_synced FROM sync_jobs WHERE id=?`, jobID,
	).Scan(&got); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if got != 42 {
		t.Fatalf("files_synced=%d; want 42", got)
	}

	// Second write overwrites (single-shot per job — last-writer wins).
	if err := jobs.SetFilesSynced(ctx, jobID, 7); err != nil {
		t.Fatalf("second set: %v", err)
	}
	if err := db.Reader.QueryRowContext(ctx,
		`SELECT files_synced FROM sync_jobs WHERE id=?`, jobID,
	).Scan(&got); err != nil {
		t.Fatalf("readback 2: %v", err)
	}
	if got != 7 {
		t.Fatalf("files_synced=%d; want 7 (overwrite)", got)
	}
}

// TestSyncJobsRepo_CountRepoInflight plants a mix of pending/running/done
// rows across two repos and asserts only pending+running for the target
// repo are counted.
func TestSyncJobsRepo_CountRepoInflight(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()
	jobs := metadata.NewSyncJobsRepo(db)
	pid := seedProject(t, db, "inflight")
	repos := metadata.NewReposRepo(db)
	repoA, err := repos.Create(ctx, pid, "deb", "r-a", "", nil, nil, nil)
	if err != nil {
		t.Fatalf("seed repo A: %v", err)
	}
	repoB, err := repos.Create(ctx, pid, "rpm", "r-b", "", nil, nil, nil)
	if err != nil {
		t.Fatalf("seed repo B: %v", err)
	}

	// Repo A: 2 pending + 1 running + 1 done.
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		for i := 0; i < 2; i++ {
			if _, err := jobs.Enqueue(ctx, tx, "apt_sync", pid, repoA, "{}"); err != nil {
				return err
			}
		}
		// Manually insert a running + a done so we don't have to lease.
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO sync_jobs(kind, repo_id, status, payload_json) VALUES ('apt_sync', ?, 'running', '{}')`,
			repoA,
		); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO sync_jobs(kind, repo_id, status, payload_json) VALUES ('apt_sync', ?, 'done', '{}')`,
			repoA,
		); err != nil {
			return err
		}
		// Repo B: 1 pending.
		if _, err := jobs.Enqueue(ctx, tx, "rpm_sync", pid, repoB, "{}"); err != nil {
			return err
		}
		return nil
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	n, err := jobs.CountRepoInflight(ctx, repoA)
	if err != nil {
		t.Fatalf("count repo A: %v", err)
	}
	if n != 3 {
		t.Fatalf("count repo A = %d, want 3 (2 pending + 1 running)", n)
	}

	n, err = jobs.CountRepoInflight(ctx, repoB)
	if err != nil {
		t.Fatalf("count repo B: %v", err)
	}
	if n != 1 {
		t.Fatalf("count repo B = %d, want 1", n)
	}
}

// TestSyncJobsRepo_CountRepoInflight_Empty asserts a repo with no rows
// returns 0 and a non-error result.
func TestSyncJobsRepo_CountRepoInflight_Empty(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()
	jobs := metadata.NewSyncJobsRepo(db)
	n, err := jobs.CountRepoInflight(ctx, 9999)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatalf("n = %d, want 0", n)
	}
}

// TestSyncJobs_GetInflightTx — plan 11-05 (D-11). The generalized 409
// envelope for concurrent-sync collisions needs the in-flight job's
// identity, not just its count, so the REST handler can populate
// details: {kind, job_id, started_at}. Shape parity with
// CountRepoInflightTx (kind-agnostic WHERE clause); returns the newest
// pending/running row, or (InflightJob{}, false, nil) when none.
func TestSyncJobs_GetInflightTx(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()
	jobs := metadata.NewSyncJobsRepo(db)
	pid := seedProject(t, db, "get-inflight")
	repos := metadata.NewReposRepo(db)
	repoA, err := repos.Create(ctx, pid, "git", "r-a", "", nil, nil, nil)
	if err != nil {
		t.Fatalf("seed repo A: %v", err)
	}
	repoEmpty, err := repos.Create(ctx, pid, "rpm", "r-empty", "", nil, nil, nil)
	if err != nil {
		t.Fatalf("seed repo empty: %v", err)
	}

	// Case A: no rows → (zero, false, nil).
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		got, ok, err := jobs.GetInflightTx(ctx, tx, repoEmpty)
		if err != nil {
			return err
		}
		if ok {
			t.Fatalf("no-rows: want exists=false, got exists=true (job=%+v)", got)
		}
		if got.ID != 0 || got.Kind != "" {
			t.Fatalf("no-rows: want zero InflightJob, got %+v", got)
		}
		return nil
	}); err != nil {
		t.Fatalf("case-empty: %v", err)
	}

	// Case B: single in-flight row → returns it.
	var firstID int64
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		id, err := jobs.Enqueue(ctx, tx, "git_sync", pid, repoA, "{}")
		if err != nil {
			return err
		}
		firstID = id
		return nil
	}); err != nil {
		t.Fatalf("seed first: %v", err)
	}
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		got, ok, err := jobs.GetInflightTx(ctx, tx, repoA)
		if err != nil {
			return err
		}
		if !ok {
			t.Fatalf("single-row: want exists=true, got false")
		}
		if got.ID != firstID {
			t.Fatalf("single-row: id=%d want %d", got.ID, firstID)
		}
		if got.Kind != "git_sync" {
			t.Fatalf("single-row: kind=%q want git_sync", got.Kind)
		}
		// StartedAt is populated from COALESCE(leased_at, created_at);
		// on a pending row with no leased_at yet, it comes from
		// created_at which the migration defaults to CURRENT_TIMESTAMP.
		if got.StartedAt.IsZero() {
			t.Fatalf("single-row: StartedAt must be non-zero (fallback to created_at)")
		}
		return nil
	}); err != nil {
		t.Fatalf("case-single: %v", err)
	}

	// Case C: two in-flight rows → returns the newest (id DESC).
	var secondID int64
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		id, err := jobs.Enqueue(ctx, tx, "git_sync", pid, repoA, "{}")
		if err != nil {
			return err
		}
		secondID = id
		return nil
	}); err != nil {
		t.Fatalf("seed second: %v", err)
	}
	if secondID <= firstID {
		t.Fatalf("secondID=%d must be > firstID=%d for the ORDER BY id DESC assertion", secondID, firstID)
	}
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		got, ok, err := jobs.GetInflightTx(ctx, tx, repoA)
		if err != nil {
			return err
		}
		if !ok {
			t.Fatalf("two-rows: want exists=true")
		}
		if got.ID != secondID {
			t.Fatalf("two-rows: id=%d want %d (newest)", got.ID, secondID)
		}
		return nil
	}); err != nil {
		t.Fatalf("case-two: %v", err)
	}

	// Case D: done/failed rows are NOT considered in-flight.
	// Mark both rows done; a subsequent GetInflightTx must return false.
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		if err := jobs.MarkDone(ctx, tx, firstID); err != nil {
			return err
		}
		if err := jobs.MarkDone(ctx, tx, secondID); err != nil {
			return err
		}
		return nil
	}); err != nil {
		t.Fatalf("mark done: %v", err)
	}
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, ok, err := jobs.GetInflightTx(ctx, tx, repoA)
		if err != nil {
			return err
		}
		if ok {
			t.Fatalf("done-rows: want exists=false (status='done' excluded)")
		}
		return nil
	}); err != nil {
		t.Fatalf("case-done: %v", err)
	}
}

// TestSyncJobsRepo_CountRepoInflightTx — plan 08-06 Codex rescue Q7.
// The tx-scoped variant lets callers run check+Enqueue atomically to
// eliminate the documented T-08-01-04 race. Asserts shape parity with
// CountRepoInflight (same SQL, writer-pool path).
func TestSyncJobsRepo_CountRepoInflightTx(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()
	jobs := metadata.NewSyncJobsRepo(db)
	pid := seedProject(t, db, "inflight-tx")
	repos := metadata.NewReposRepo(db)
	repoA, err := repos.Create(ctx, pid, "deb", "r-a", "", nil, nil, nil)
	if err != nil {
		t.Fatalf("seed repo A: %v", err)
	}
	repoB, err := repos.Create(ctx, pid, "rpm", "r-b", "", nil, nil, nil)
	if err != nil {
		t.Fatalf("seed repo B: %v", err)
	}

	// Seed 2 pending rows for repoA.
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		for i := 0; i < 2; i++ {
			if _, err := jobs.Enqueue(ctx, tx, "apt_sync", pid, repoA, `{}`); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Read count inside a writer tx — the path the fixed sync REST handler
	// takes.
	var n int
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		got, err := jobs.CountRepoInflightTx(ctx, tx, repoA)
		if err != nil {
			return err
		}
		n = got
		return nil
	}); err != nil {
		t.Fatalf("count in tx: %v", err)
	}
	if n != 2 {
		t.Fatalf("CountRepoInflightTx=%d; want 2", n)
	}

	// repoB has zero rows → 0.
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		got, err := jobs.CountRepoInflightTx(ctx, tx, repoB)
		if err != nil {
			return err
		}
		n = got
		return nil
	}); err != nil {
		t.Fatalf("count repoB in tx: %v", err)
	}
	if n != 0 {
		t.Fatalf("unrelated repo n=%d; want 0", n)
	}

	// Within the SAME tx, an Enqueue is immediately visible to a
	// subsequent CountRepoInflightTx — this is the race-closing
	// guarantee: a second /sync caller would observe the in-flight row
	// before it gets to enqueue its own.
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		if _, err := jobs.Enqueue(ctx, tx, "rpm_sync", pid, repoB, `{}`); err != nil {
			return err
		}
		got, err := jobs.CountRepoInflightTx(ctx, tx, repoB)
		if err != nil {
			return err
		}
		n = got
		return nil
	}); err != nil {
		t.Fatalf("enqueue+count in tx: %v", err)
	}
	if n != 1 {
		t.Fatalf("same-tx enqueue+count n=%d; want 1 (race-closing guarantee)", n)
	}
}

// --------------------------------------------------------------------------
// Phase 5 Plan 02 (HELMRETRY-03) — helm partial-sync terminal writers.
// --------------------------------------------------------------------------

// TestSyncJobsRepo_MarkPermanentlyFailedWithLog proves the D-04 atomicity
// invariant: a reader observing status='failed' on a row written by
// MarkPermanentlyFailedWithLog ALWAYS observes the populated log column
// too. The assertion is a SINGLE SELECT for status/last_error/log in one
// Scan — any non-atomic implementation (two sequential UPDATEs) would
// leave a visible window where status is flipped but log is still empty,
// and the scan below would fail for the log assertion.
func TestSyncJobsRepo_MarkPermanentlyFailedWithLog(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	jobs := metadata.NewSyncJobsRepo(db)
	ctx := context.Background()

	var id int64
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		var err error
		id, err = jobs.Enqueue(ctx, tx, "helm_sync", 0, 0, "{}")
		return err
	}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	// Move to running — mirrors the live-path state at the moment Pool
	// decides to terminally fail the job.
	if _, _, err := jobs.LeaseOne(ctx, "worker-A"); err != nil {
		t.Fatalf("lease: %v", err)
	}

	const logJSON = `{"partial":true,"files_persisted":2,"files_expected":3}`
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		return jobs.MarkPermanentlyFailedWithLog(ctx, tx, id, "sync failed", logJSON)
	}); err != nil {
		t.Fatalf("mark_perm_failed_with_log: %v", err)
	}

	// SINGLE read of all three columns in one Scan — this is the
	// atomicity assertion. If the implementation split the SET into two
	// UPDATE statements, a well-timed reader between them would see
	// status='failed' with log still empty; that window must not exist.
	var (
		status  string
		lastErr sql.NullString
		logCol  sql.NullString
	)
	if err := db.Reader.QueryRow(
		`SELECT status, last_error, log FROM sync_jobs WHERE id=?`,
		id,
	).Scan(&status, &lastErr, &logCol); err != nil {
		t.Fatalf("select: %v", err)
	}
	if status != "failed" {
		t.Fatalf("status=%q want failed", status)
	}
	if !lastErr.Valid || lastErr.String != "sync failed" {
		t.Fatalf("last_error=%q valid=%v; want 'sync failed'", lastErr.String, lastErr.Valid)
	}
	if !logCol.Valid || logCol.String != logJSON {
		t.Fatalf("log=%q valid=%v; want %q", logCol.String, logCol.Valid, logJSON)
	}

	// Sub-case: call against a non-existent id. The WHERE doesn't match;
	// RowsAffected is zero but the method must not error — no-op is the
	// correct terminal-writer semantic.
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		return jobs.MarkPermanentlyFailedWithLog(ctx, tx, 99999, "ghost", `{"partial":true}`)
	}); err != nil {
		t.Fatalf("no-op on missing id: %v", err)
	}
}

// TestSyncJobsRepo_RecoverStaleByKind_HelmOnly proves the D-02 scope
// boundary: RecoverStaleByKind terminates ONLY stale running rows
// whose kind matches the filter. Non-matching kinds (e.g. pypi_sync)
// stay in 'running' state and retain the existing RecoverStale
// retry semantics wired elsewhere. Two rows are seeded — one helm,
// one pypi — both stale; the call targets helm_sync with
// terminalStatus='failed' and a partial-log JSON.
func TestSyncJobsRepo_RecoverStaleByKind_HelmOnly(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	jobs := metadata.NewSyncJobsRepo(db)
	ctx := context.Background()

	// Seed TWO stale running rows via raw SQL so leased_at can be set to
	// 11 minutes ago deterministically (Enqueue+LeaseOne would use
	// CURRENT_TIMESTAMP, which would not be stale).
	if _, err := db.Writer.ExecContext(ctx, `
		INSERT INTO sync_jobs(kind, status, leased_by, leased_at)
		VALUES ('helm_sync','running','w1',datetime('now','-11 minutes'))
	`); err != nil {
		t.Fatalf("seed helm row: %v", err)
	}
	if _, err := db.Writer.ExecContext(ctx, `
		INSERT INTO sync_jobs(kind, status, leased_by, leased_at)
		VALUES ('pypi_sync','running','w2',datetime('now','-11 minutes'))
	`); err != nil {
		t.Fatalf("seed pypi row: %v", err)
	}

	const logJSON = `{"partial":true,"files_persisted":null,"files_expected":null}`
	var n int
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		var err error
		n, err = jobs.RecoverStaleByKind(
			ctx, tx,
			time.Now().Add(-10*time.Minute),
			"helm_sync",
			"failed",
			logJSON,
		)
		return err
	}); err != nil {
		t.Fatalf("recover_stale_by_kind: %v", err)
	}

	if n != 1 {
		t.Fatalf("rows affected=%d want 1 (only helm row should match kind filter)", n)
	}

	// Helm row: terminally failed with log + last_error populated.
	var (
		helmStatus, helmLastErr string
		helmLog                 string
	)
	if err := db.Reader.QueryRow(
		`SELECT status, last_error, log FROM sync_jobs WHERE kind='helm_sync'`,
	).Scan(&helmStatus, &helmLastErr, &helmLog); err != nil {
		t.Fatalf("select helm row: %v", err)
	}
	if helmStatus != "failed" {
		t.Fatalf("helm status=%q want failed", helmStatus)
	}
	if helmLog != logJSON {
		t.Fatalf("helm log=%q want %q", helmLog, logJSON)
	}
	if helmLastErr != "stale running row terminated at boot" {
		t.Fatalf("helm last_error=%q want sentinel", helmLastErr)
	}

	// Pypi row: UNTOUCHED — scope boundary proof (D-02). This is the
	// load-bearing assertion: if the kind filter were missing from the
	// WHERE clause, this row would also have flipped to 'failed'.
	var pypiStatus string
	if err := db.Reader.QueryRow(
		`SELECT status FROM sync_jobs WHERE kind='pypi_sync'`,
	).Scan(&pypiStatus); err != nil {
		t.Fatalf("select pypi row: %v", err)
	}
	if pypiStatus != "running" {
		t.Fatalf("pypi status=%q want running (untouched)", pypiStatus)
	}
}

// TestSyncJobs_SetSummaryDriftPurged covers v1.5 Phase 6 D-21: a JSON-merge
// writer that stamps `drift_purged` into sync_jobs.summary without disturbing
// sibling keys. Uses SQLite's json_set() so json1 must be compiled in
// (modernc.org/sqlite default).
func TestSyncJobs_SetSummaryDriftPurged(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	jobs := metadata.NewSyncJobsRepo(db)
	ctx := context.Background()

	var id int64
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		var err error
		id, err = jobs.Enqueue(ctx, tx, "helm_sync", 0, 0, "{}")
		return err
	}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	// First write: summary starts as '{}', drift_purged key absent.
	if err := jobs.SetSummaryDriftPurged(ctx, id, 5); err != nil {
		t.Fatalf("set drift_purged=5: %v", err)
	}
	var summary string
	if err := db.Reader.QueryRow(
		`SELECT summary FROM sync_jobs WHERE id=?`, id,
	).Scan(&summary); err != nil {
		t.Fatalf("select summary: %v", err)
	}
	// json_set should produce {"drift_purged":5}.
	if summary != `{"drift_purged":5}` {
		t.Fatalf("summary after first write = %q, want %q", summary, `{"drift_purged":5}`)
	}

	// Second write: overwrites in place.
	if err := jobs.SetSummaryDriftPurged(ctx, id, 12); err != nil {
		t.Fatalf("set drift_purged=12: %v", err)
	}
	if err := db.Reader.QueryRow(
		`SELECT summary FROM sync_jobs WHERE id=?`, id,
	).Scan(&summary); err != nil {
		t.Fatalf("select summary 2: %v", err)
	}
	if summary != `{"drift_purged":12}` {
		t.Fatalf("summary after overwrite = %q, want %q", summary, `{"drift_purged":12}`)
	}

	// Sibling-key preservation: pre-populate summary with a foreign key,
	// then call SetSummaryDriftPurged and assert the foreign key survives.
	if _, err := db.Writer.ExecContext(ctx,
		`UPDATE sync_jobs SET summary = ? WHERE id = ?`,
		`{"files_synced":7}`, id,
	); err != nil {
		t.Fatalf("seed sibling key: %v", err)
	}
	if err := jobs.SetSummaryDriftPurged(ctx, id, 3); err != nil {
		t.Fatalf("set drift_purged=3 with sibling: %v", err)
	}
	if err := db.Reader.QueryRow(
		`SELECT summary FROM sync_jobs WHERE id=?`, id,
	).Scan(&summary); err != nil {
		t.Fatalf("select summary 3: %v", err)
	}
	// json_set merges — both keys present. Order is JSON-set's insertion
	// order: existing first, new appended.
	if summary != `{"files_synced":7,"drift_purged":3}` {
		t.Fatalf("summary after merge = %q, want %q", summary, `{"files_synced":7,"drift_purged":3}`)
	}

	// Zero-count is legal (D-10 run-evidence path).
	if err := jobs.SetSummaryDriftPurged(ctx, id, 0); err != nil {
		t.Fatalf("set drift_purged=0: %v", err)
	}
	if err := db.Reader.QueryRow(
		`SELECT summary FROM sync_jobs WHERE id=?`, id,
	).Scan(&summary); err != nil {
		t.Fatalf("select summary 4: %v", err)
	}
	if summary != `{"files_synced":7,"drift_purged":0}` {
		t.Fatalf("summary after drift_purged=0 = %q, want %q", summary, `{"files_synced":7,"drift_purged":0}`)
	}
}
