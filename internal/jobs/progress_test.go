package jobs_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dxc-internal/omnirepo/internal/jobs"
)

// fakeProgressRepo records every SetProgress call for assertion.
type fakeProgressRepo struct {
	calls []progressCall
	err   error // when non-nil, SetProgress returns err and does NOT record
}

type progressCall struct {
	jobID      int64
	step       string
	done, total int64
}

func (f *fakeProgressRepo) SetProgress(_ context.Context, jobID int64, step string, done, total int64) error {
	if f.err != nil {
		return f.err
	}
	f.calls = append(f.calls, progressCall{jobID, step, done, total})
	return nil
}

// newWriter builds a writer with a manually-advanced fake clock.
// Returns the writer, the repo, and a *time.Time the caller mutates.
func newWriter(t *testing.T, jobID int64) (*jobs.ProgressWriter, *fakeProgressRepo, *time.Time) {
	t.Helper()
	repo := &fakeProgressRepo{}
	w := jobs.NewProgressWriter(repo, jobID)
	now := time.Unix(0, 0)
	clock := &now
	w.SetNow(func() time.Time { return *clock })
	return w, repo, clock
}

func TestProgressWriter_FirstCallAlwaysWrites(t *testing.T) {
	w, repo, _ := newWriter(t, 42)
	if err := w.Set(context.Background(), "a", 1, 10); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if len(repo.calls) != 1 {
		t.Fatalf("calls = %d; want 1 (first-call-always-writes)", len(repo.calls))
	}
	got := repo.calls[0]
	if got.jobID != 42 || got.step != "a" || got.done != 1 || got.total != 10 {
		t.Fatalf("call = %+v; want {42 a 1 10}", got)
	}
}

func TestProgressWriter_ThrottleSuppresses(t *testing.T) {
	w, repo, clock := newWriter(t, 7)
	// First write lands.
	if err := w.Set(context.Background(), "a", 1, 10); err != nil {
		t.Fatalf("Set 1: %v", err)
	}
	// +50 ms: should suppress (change but too soon).
	*clock = clock.Add(50 * time.Millisecond)
	if err := w.Set(context.Background(), "a", 2, 10); err != nil {
		t.Fatalf("Set 2: %v", err)
	}
	if len(repo.calls) != 1 {
		t.Fatalf("after +50ms calls = %d; want 1 (throttled)", len(repo.calls))
	}
	// +250 ms (total 300 ms): should now write.
	*clock = clock.Add(250 * time.Millisecond)
	if err := w.Set(context.Background(), "a", 3, 10); err != nil {
		t.Fatalf("Set 3: %v", err)
	}
	if len(repo.calls) != 2 {
		t.Fatalf("after +300ms calls = %d; want 2", len(repo.calls))
	}
	if got := repo.calls[1]; got.done != 3 {
		t.Fatalf("call[1].done = %d; want 3", got.done)
	}
}

func TestProgressWriter_ChangeDetectSuppresses(t *testing.T) {
	w, repo, clock := newWriter(t, 9)
	if err := w.Set(context.Background(), "a", 1, 10); err != nil {
		t.Fatalf("Set 1: %v", err)
	}
	// Advance enough that the throttle would normally allow a write,
	// but the triple is identical → change-detect suppresses.
	*clock = clock.Add(300 * time.Millisecond)
	if err := w.Set(context.Background(), "a", 1, 10); err != nil {
		t.Fatalf("Set 2: %v", err)
	}
	if len(repo.calls) != 1 {
		t.Fatalf("calls = %d; want 1 (change-detect suppression)", len(repo.calls))
	}
}

func TestProgressWriter_StepChangeWritesEvenWhenBytesSame(t *testing.T) {
	w, repo, clock := newWriter(t, 11)
	if err := w.Set(context.Background(), "a", 5, 10); err != nil {
		t.Fatalf("Set 1: %v", err)
	}
	*clock = clock.Add(250 * time.Millisecond)
	// Same bytes, different step → must write.
	if err := w.Set(context.Background(), "b", 5, 10); err != nil {
		t.Fatalf("Set 2: %v", err)
	}
	if len(repo.calls) != 2 {
		t.Fatalf("calls = %d; want 2 (step change)", len(repo.calls))
	}
	if got := repo.calls[1]; got.step != "b" {
		t.Fatalf("call[1].step = %q; want b", got.step)
	}
}

func TestProgressWriter_FlushBypassesThrottle(t *testing.T) {
	w, repo, clock := newWriter(t, 13)
	if err := w.Set(context.Background(), "final", 10, 10); err != nil {
		t.Fatalf("Set: %v", err)
	}
	// Only 50 ms elapsed — Set would throttle here, but Flush must not.
	*clock = clock.Add(50 * time.Millisecond)
	if err := w.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if len(repo.calls) != 2 {
		t.Fatalf("calls = %d; want 2 (Set + Flush)", len(repo.calls))
	}
	if got := repo.calls[1]; got.step != "final" || got.done != 10 || got.total != 10 {
		t.Fatalf("flush call = %+v; want {_ final 10 10}", got)
	}
}

func TestProgressWriter_FlushWithoutPriorSetIsNoOp(t *testing.T) {
	w, repo, _ := newWriter(t, 15)
	if err := w.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if len(repo.calls) != 0 {
		t.Fatalf("calls = %d; want 0 (flush with no prior Set is no-op)", len(repo.calls))
	}
}

func TestProgressWriter_ReturnsDBError(t *testing.T) {
	repo := &fakeProgressRepo{err: errors.New("write failed")}
	w := jobs.NewProgressWriter(repo, 1)
	err := w.Set(context.Background(), "a", 1, 10)
	if err == nil || err.Error() != "write failed" {
		t.Fatalf("Set err = %v; want write failed", err)
	}
	// After a failed write, last-state is NOT advanced — next call should
	// still attempt (fresh first-call). Clear the err on the fake and confirm.
	repo.err = nil
	if err := w.Set(context.Background(), "a", 1, 10); err != nil {
		t.Fatalf("Set retry: %v", err)
	}
	if len(repo.calls) != 1 {
		t.Fatalf("calls = %d; want 1 (only the retry lands)", len(repo.calls))
	}
}

func TestProgressWriter_NilRepoIsNoOp(t *testing.T) {
	w := jobs.NewProgressWriter(nil, 1)
	if err := w.Set(context.Background(), "a", 1, 10); err != nil {
		t.Fatalf("Set on nil-repo writer: %v", err)
	}
	if err := w.Flush(context.Background()); err != nil {
		t.Fatalf("Flush on nil-repo writer: %v", err)
	}
}

// TestProgressWriter_ClampsHugeStep — plan 08-06 Codex rescue Q5.
// Upstream-controlled text (package names, filenames) flows into the
// step at APT/RPM/PyPI/Helm. A hostile upstream with a megabyte-long
// package name could otherwise blow up sync_jobs.current_step. The
// writer clamps at MaxStepLen bytes with a trailing "…" marker.
func TestProgressWriter_ClampsHugeStep(t *testing.T) {
	w, repo, _ := newWriter(t, 1)
	// 100 KiB of A — far over the 1 KiB cap.
	huge := make([]byte, 100*1024)
	for i := range huge {
		huge[i] = 'A'
	}
	if err := w.Set(context.Background(), string(huge), 0, 0); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if len(repo.calls) != 1 {
		t.Fatalf("calls = %d; want 1", len(repo.calls))
	}
	got := repo.calls[0].step
	if len(got) > jobs.MaxStepLen {
		t.Fatalf("persisted step length %d > MaxStepLen %d", len(got), jobs.MaxStepLen)
	}
	// The persisted step should be strictly shorter than the input (we
	// truncated) AND end with the ellipsis marker so the operator sees
	// the clamp.
	if len(got) >= len(huge) {
		t.Fatalf("step not truncated: len=%d", len(got))
	}
	if got[len(got)-len("…"):] != "…" {
		t.Fatalf("step missing ellipsis marker; last bytes = %q", got[len(got)-3:])
	}
}

// TestProgressWriter_ShortStepNotClamped — normal-sized steps pass
// through byte-for-byte. Critical: no code-path change for the 99.9%
// of real-world step strings (package names max out ~80 bytes).
func TestProgressWriter_ShortStepNotClamped(t *testing.T) {
	w, repo, _ := newWriter(t, 1)
	short := "pulling redis_7.0.0"
	if err := w.Set(context.Background(), short, 100, 200); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if len(repo.calls) != 1 {
		t.Fatalf("calls = %d; want 1", len(repo.calls))
	}
	if repo.calls[0].step != short {
		t.Fatalf("step = %q; want %q", repo.calls[0].step, short)
	}
}
