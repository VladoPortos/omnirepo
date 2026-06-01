package driftpurge

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

// fakeRow implements Row.
type fakeRow struct {
	key      Key
	filename string
}

func (r *fakeRow) Key() Key               { return r.key }
func (r *fakeRow) SampleFilename() string { return r.filename }

// fakeAdapter implements DriftAdapter. UpstreamKeys + LocalRows are
// canned; Purge records calls for assertions.
type fakeAdapter struct {
	protocol   string
	trashKind  string
	upstream   []Key
	localRows  []Row
	localErr   error
	purgeErrAt int // 1-indexed; 0 means never error
	purgeCalls []Row
}

func (a *fakeAdapter) Protocol() string  { return a.protocol }
func (a *fakeAdapter) TrashKind() string { return a.trashKind }
func (a *fakeAdapter) UpstreamKeys() []Key {
	return a.upstream
}
func (a *fakeAdapter) LocalRows(_ context.Context, _ *sql.Tx, _ int64) ([]Row, error) {
	return a.localRows, a.localErr
}
func (a *fakeAdapter) Purge(_ context.Context, _ *sql.Tx, row Row, _ string) error {
	a.purgeCalls = append(a.purgeCalls, row)
	if a.purgeErrAt > 0 && len(a.purgeCalls) == a.purgeErrAt {
		return errors.New("fake purge error")
	}
	return nil
}

func mkRow(name string) Row {
	return &fakeRow{key: Key{A: name}, filename: name + ".tgz"}
}

func mkKey(name string) Key { return Key{A: name} }

func TestRun_EmptyUpstreamGuard(t *testing.T) {
	a := &fakeAdapter{
		protocol:  "test",
		upstream:  []Key{},
		localRows: []Row{mkRow("a"), mkRow("b"), mkRow("c")},
	}
	rep, err := Run(context.Background(), nil, 42, "alice", a, 0, false)
	if err != nil {
		t.Fatalf("Run returned err: %v", err)
	}
	if !rep.Skipped {
		t.Errorf("Skipped = false, want true")
	}
	if rep.Reason != "upstream_empty" {
		t.Errorf("Reason = %q, want upstream_empty", rep.Reason)
	}
	if rep.LocalCount != 3 {
		t.Errorf("LocalCount = %d, want 3", rep.LocalCount)
	}
	if rep.PurgedCount != 0 {
		t.Errorf("PurgedCount = %d, want 0", rep.PurgedCount)
	}
	if len(a.purgeCalls) != 0 {
		t.Errorf("Purge called %d times, want 0 (guard must short-circuit)", len(a.purgeCalls))
	}
}

func TestRun_EmptyUpstream_EmptyLocal(t *testing.T) {
	a := &fakeAdapter{protocol: "test", upstream: []Key{}, localRows: []Row{}}
	rep, err := Run(context.Background(), nil, 1, "", a, 0, false)
	if err != nil {
		t.Fatalf("Run err: %v", err)
	}
	if rep.Skipped {
		t.Errorf("Skipped = true, want false (no rows to protect)")
	}
	if rep.PurgedCount != 0 {
		t.Errorf("PurgedCount = %d, want 0", rep.PurgedCount)
	}
}

func TestRun_NoDrift(t *testing.T) {
	a := &fakeAdapter{
		protocol:  "test",
		upstream:  []Key{mkKey("a"), mkKey("b"), mkKey("c")},
		localRows: []Row{mkRow("a"), mkRow("b"), mkRow("c")},
	}
	rep, err := Run(context.Background(), nil, 1, "", a, 0, false)
	if err != nil {
		t.Fatalf("Run err: %v", err)
	}
	if rep.PurgedCount != 0 {
		t.Errorf("PurgedCount = %d, want 0", rep.PurgedCount)
	}
	if len(rep.Sample) != 0 {
		t.Errorf("Sample = %v, want empty", rep.Sample)
	}
	if len(a.purgeCalls) != 0 {
		t.Errorf("Purge called %d times, want 0", len(a.purgeCalls))
	}
}

func TestRun_FullDrift(t *testing.T) {
	a := &fakeAdapter{
		protocol:  "test",
		upstream:  []Key{mkKey("a")},
		localRows: []Row{mkRow("a"), mkRow("d"), mkRow("e")},
	}
	rep, err := Run(context.Background(), nil, 1, "", a, 0, false)
	if err != nil {
		t.Fatalf("Run err: %v", err)
	}
	if rep.PurgedCount != 2 {
		t.Errorf("PurgedCount = %d, want 2", rep.PurgedCount)
	}
	wantSample := []string{"d.tgz", "e.tgz"}
	if len(rep.Sample) != 2 || rep.Sample[0] != wantSample[0] || rep.Sample[1] != wantSample[1] {
		t.Errorf("Sample = %v, want %v", rep.Sample, wantSample)
	}
	if len(a.purgeCalls) != 2 {
		t.Errorf("Purge called %d times, want 2", len(a.purgeCalls))
	}
}

func TestRun_Sample20Cap(t *testing.T) {
	// Upstream {a}; Local = a + 25 others (b..z). Drift = 25 rows; Sample = first 20 lex.
	upstream := []Key{mkKey("a")}
	local := []Row{mkRow("a")}
	for c := 'b'; c <= 'z'; c++ {
		local = append(local, mkRow(string(c)))
	}
	// 1 ('a' in upstream) + 25 (b..z) = 26 local rows; drift = 25.
	a := &fakeAdapter{protocol: "test", upstream: upstream, localRows: local}
	rep, err := Run(context.Background(), nil, 1, "", a, 0, false)
	if err != nil {
		t.Fatalf("Run err: %v", err)
	}
	if rep.PurgedCount != 25 {
		t.Errorf("PurgedCount = %d, want 25", rep.PurgedCount)
	}
	if len(rep.Sample) != 20 {
		t.Errorf("Sample len = %d, want 20 (cap)", len(rep.Sample))
	}
	// Sample should be b.tgz..u.tgz (first 20 lex after a).
	if rep.Sample[0] != "b.tgz" || rep.Sample[19] != "u.tgz" {
		t.Errorf("Sample bounds wrong: first=%q last=%q", rep.Sample[0], rep.Sample[19])
	}
}

func TestRun_SampleLexOrderFromScrambledInput(t *testing.T) {
	a := &fakeAdapter{
		protocol:  "test",
		upstream:  []Key{},
		localRows: nil, // filled below
	}
	// Upstream non-empty so guard doesn't trip — add one matching key
	// then scramble local.
	a.upstream = []Key{mkKey("z-keep")}
	a.localRows = []Row{
		mkRow("z-keep"),
		mkRow("zebra"),
		mkRow("apple"),
		mkRow("mango"),
	}
	rep, err := Run(context.Background(), nil, 1, "", a, 0, false)
	if err != nil {
		t.Fatalf("Run err: %v", err)
	}
	want := []string{"apple.tgz", "mango.tgz", "zebra.tgz"}
	if len(rep.Sample) != len(want) {
		t.Fatalf("Sample len = %d, want %d (%v)", len(rep.Sample), len(want), rep.Sample)
	}
	for i := range want {
		if rep.Sample[i] != want[i] {
			t.Errorf("Sample[%d] = %q, want %q", i, rep.Sample[i], want[i])
		}
	}
}

func TestRun_LocalRowsError(t *testing.T) {
	a := &fakeAdapter{
		protocol: "test",
		upstream: []Key{mkKey("a")},
		localErr: errors.New("db boom"),
	}
	_, err := Run(context.Background(), nil, 1, "", a, 0, false)
	if err == nil {
		t.Fatal("Run err = nil, want non-nil")
	}
	if !strings.Contains(err.Error(), "driftpurge: load local rows") {
		t.Errorf("err = %v, want to contain 'driftpurge: load local rows'", err)
	}
}

// TestRun_ThresholdGuard_Trips — percent-threshold guard (v1.7).
// Threshold 50, 4 local rows, 3 of which drift (75%). Expect Skipped
// with reason="threshold_exceeded" and BlockedCount=3; Purge MUST not
// be called.
func TestRun_ThresholdGuard_Trips(t *testing.T) {
	a := &fakeAdapter{
		protocol:  "test",
		upstream:  []Key{mkKey("keep")},
		localRows: []Row{mkRow("keep"), mkRow("a"), mkRow("b"), mkRow("c")},
	}
	rep, err := Run(context.Background(), nil, 1, "", a, 50, false)
	if err != nil {
		t.Fatalf("Run err: %v", err)
	}
	if !rep.Skipped {
		t.Errorf("Skipped = false, want true (3/4 drift > 50%%)")
	}
	if rep.Reason != "threshold_exceeded" {
		t.Errorf("Reason = %q, want threshold_exceeded", rep.Reason)
	}
	if rep.BlockedCount != 3 {
		t.Errorf("BlockedCount = %d, want 3", rep.BlockedCount)
	}
	if len(a.purgeCalls) != 0 {
		t.Errorf("Purge called %d times, want 0 (threshold guard must short-circuit)", len(a.purgeCalls))
	}
}

// TestRun_ThresholdGuard_BoundaryAtThreshold — exactly at threshold
// (drift*100 == threshold*local) is NOT blocked (strict inequality).
// 2 of 4 = 50% with threshold 50 → proceeds.
func TestRun_ThresholdGuard_BoundaryAtThreshold(t *testing.T) {
	a := &fakeAdapter{
		protocol:  "test",
		upstream:  []Key{mkKey("a"), mkKey("b")},
		localRows: []Row{mkRow("a"), mkRow("b"), mkRow("c"), mkRow("d")},
	}
	rep, err := Run(context.Background(), nil, 1, "", a, 50, false)
	if err != nil {
		t.Fatalf("Run err: %v", err)
	}
	if rep.Skipped {
		t.Errorf("Skipped = true, want false (50%% == threshold; strict > only)")
	}
	if rep.PurgedCount != 2 {
		t.Errorf("PurgedCount = %d, want 2", rep.PurgedCount)
	}
}

// TestRun_ThresholdGuard_DisabledByZero — thresholdPct=0 disables the
// guard entirely so even 100% drift purges through.
func TestRun_ThresholdGuard_DisabledByZero(t *testing.T) {
	a := &fakeAdapter{
		protocol:  "test",
		upstream:  []Key{mkKey("nope")}, // no local row matches
		localRows: []Row{mkRow("a"), mkRow("b"), mkRow("c")},
	}
	rep, err := Run(context.Background(), nil, 1, "", a, 0, false)
	if err != nil {
		t.Fatalf("Run err: %v", err)
	}
	if rep.Skipped {
		t.Errorf("Skipped = true, want false (thresholdPct=0 disables guard)")
	}
	if rep.PurgedCount != 3 {
		t.Errorf("PurgedCount = %d, want 3", rep.PurgedCount)
	}
}

// TestRun_ThresholdGuard_ForceBypasses — force=true bypasses the
// threshold guard regardless of how high the drift fraction is.
func TestRun_ThresholdGuard_ForceBypasses(t *testing.T) {
	a := &fakeAdapter{
		protocol:  "test",
		upstream:  []Key{mkKey("keep")},
		localRows: []Row{mkRow("keep"), mkRow("a"), mkRow("b"), mkRow("c"), mkRow("d")},
	}
	// 4/5 drift = 80% > 50 threshold; force=true bypasses.
	rep, err := Run(context.Background(), nil, 1, "", a, 50, true)
	if err != nil {
		t.Fatalf("Run err: %v", err)
	}
	if rep.Skipped {
		t.Errorf("Skipped = true, want false (force=true bypasses threshold)")
	}
	if rep.PurgedCount != 4 {
		t.Errorf("PurgedCount = %d, want 4", rep.PurgedCount)
	}
	if rep.BlockedCount != 0 {
		t.Errorf("BlockedCount = %d, want 0 (no block fired)", rep.BlockedCount)
	}
}

// TestRun_ThresholdGuard_LocalEmpty_NoBlock — guard only meaningful
// when local has rows. An empty local repo with empty upstream stays
// the no-op path; an empty local with non-empty upstream is also a
// no-op (no drift possible).
func TestRun_ThresholdGuard_LocalEmpty_NoBlock(t *testing.T) {
	a := &fakeAdapter{
		protocol:  "test",
		upstream:  []Key{mkKey("a"), mkKey("b")},
		localRows: []Row{},
	}
	rep, err := Run(context.Background(), nil, 1, "", a, 50, false)
	if err != nil {
		t.Fatalf("Run err: %v", err)
	}
	if rep.Skipped {
		t.Errorf("Skipped = true, want false (empty local cannot trip threshold)")
	}
}

// TestRun_ThresholdGuard_OrderedAfterEmptyUpstream — the empty-
// upstream guard takes precedence over the threshold guard so
// the existing audit reason stays "upstream_empty" for that case.
func TestRun_ThresholdGuard_OrderedAfterEmptyUpstream(t *testing.T) {
	a := &fakeAdapter{
		protocol:  "test",
		upstream:  []Key{},
		localRows: []Row{mkRow("a"), mkRow("b")},
	}
	rep, err := Run(context.Background(), nil, 1, "", a, 50, false)
	if err != nil {
		t.Fatalf("Run err: %v", err)
	}
	if !rep.Skipped {
		t.Fatalf("Skipped = false, want true")
	}
	if rep.Reason != "upstream_empty" {
		t.Errorf("Reason = %q, want upstream_empty (must precede threshold check)", rep.Reason)
	}
}

func TestRun_PurgeErrorMidIteration(t *testing.T) {
	// 3 drift rows; 3rd Purge call errors; report PurgedCount should
	// reflect the 2 that landed before the error.
	a := &fakeAdapter{
		protocol:   "test",
		upstream:   []Key{mkKey("keep")},
		localRows:  []Row{mkRow("keep"), mkRow("a"), mkRow("b"), mkRow("c")},
		purgeErrAt: 3,
	}
	rep, err := Run(context.Background(), nil, 1, "", a, 0, false)
	if err == nil {
		t.Fatal("Run err = nil, want non-nil on Purge failure")
	}
	if rep.PurgedCount != 2 {
		t.Errorf("PurgedCount = %d, want 2 (2 succeeded before 3rd failed)", rep.PurgedCount)
	}
	if len(a.purgeCalls) != 3 {
		t.Errorf("purge calls = %d, want 3 (iterate until error)", len(a.purgeCalls))
	}
}
