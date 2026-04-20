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
