// Package driftpurge implements the protocol-agnostic drift-detection
// engine for mirror repos.
//
// The engine compares (parsed upstream entries) against (local DB rows)
// and invokes an adapter to soft-delete rows whose upstream key has
// vanished. The empty-upstream safety guard prevents a
// misparsed / empty upstream response from wiping an entire mirror.
//
// Protocol knowledge lives entirely in DriftAdapter implementations
// (one per protocol in this package). The engine imports zero
// protocol packages, which keeps the layering: sync_handler ->
// driftpurge.Run -> adapter. Adapters may import protocol rowrepos
// (internal/metadata/*) but the engine does not.
package driftpurge

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"

	"github.com/vladoportos/omnirepo/internal/storage"
)

// Key is the comparable identity tuple for a drift diff. Fields are
// stringified by the adapter so map[Key]struct{} lookups are O(1).
// Per-protocol key shapes:
//
//	PyPI: {A: project_normalized, B: filename, C: digest}
//	RPM:  {A: name, B: version, C: arch}
//	APT:  {A: name+"|"+component+"|"+suite, B: version, C: arch}
//	Helm: {A: name, B: version, C: ""}
//
// Adapters MUST use consistent stringification between UpstreamKeys()
// and LocalRows()[i].Key() or the diff will be vacuous.
type Key struct {
	A, B, C string
}

// Row is the engine-facing projection of a local DB row that may be
// drift-purged. Implementations live in per-protocol adapters and
// wrap concrete *metadata.PyPIFile / *metadata.RPMPackage / etc.
type Row interface {
	// Key returns the engine comparison tuple. MUST be identical
	// to the stringification used in adapter.UpstreamKeys().
	Key() Key
	// SampleFilename is the short human-readable label used in the
	// mirror.drift_purged audit `sample` array. PyPI returns
	// filename; helm returns name-version.tgz; RPM/APT return NEVRA.
	SampleFilename() string
}

// DriftReport is the outcome of a single driftpurge.Run call.
//
// Skipped=true means a safety guard tripped and adapter.Purge was NOT
// invoked. Reason carries the guard token:
//   - "upstream_empty"      — the empty-upstream guard
//   - "threshold_exceeded"  — the v1.7 percent-threshold guard.
//     BlockedCount carries the
//     would-purge count so the caller can
//     stamp sync_jobs.summary.drift_blocked
//     and surface an admin-confirm override.
//
// LocalCount carries the number of rows that WOULD have been
// vulnerable so the caller can emit mirror.drift_purge_skipped with
// diagnostics.
//
// PurgedCount is the number of rows successfully purged. On partial
// failure (adapter.Purge returned an error mid-iteration) PurgedCount
// reflects the rows that landed in trash before the error; callers
// decide whether to treat this as a full sync failure. The engine
// never rolls back — the caller owns the tx.
//
// Sample is lex-sorted first-20 of drift filenames,
// populated on successful full-iteration runs (Skipped=false, err=nil).
// On partial failure it contains the sample computed BEFORE iteration
// started (so audit can still emit the intended sample).
//
// BlockedCount is non-zero only when Skipped=true and
// Reason=="threshold_exceeded": the count of rows the guard prevented
// from being purged.
type DriftReport struct {
	Protocol     string
	PurgedCount  int
	Sample       []string
	Skipped      bool
	Reason       string
	LocalCount   int
	BlockedCount int
}

// DriftAdapter is implemented by each per-protocol adapter (see
// pypi_adapter.go etc. in this package). It is the ONLY seam between
// the protocol-agnostic engine and protocol-specific row/key shape.
type DriftAdapter interface {
	// Protocol returns the short name ("pypi"|"rpm"|"deb"|"helm")
	// used in audit details_json.
	Protocol() string
	// TrashKind returns the Trash `kind` string ("pypi_file_drift"
	// etc.) used when the adapter's Purge writes a trash
	// holder via storage.Trash.MoveWithSnapshot.
	TrashKind() string
	// UpstreamKeys returns the parsed upstream entries' drift keys.
	// Called once per Run; result is converted to a set.
	UpstreamKeys() []Key
	// LocalRows loads every local row for repoID that participates
	// in drift. Called once per Run; ordering does not matter —
	// the engine sorts by SampleFilename for the Sample output.
	LocalRows(ctx context.Context, tx *sql.Tx, repoID int64) ([]Row, error)
	// Purge DELETEs one drift row inside the caller's tx and returns the
	// deferred file relocation (PendingMove) that the caller must apply
	// AFTER the tx commits. The on-disk move is NOT performed inside Purge:
	// it is irreversible, so doing it mid-tx would let a later rollback
	// strand a restored row against an already-trashed file. Called once
	// per drift row; failures propagate up via Run's error return.
	Purge(ctx context.Context, tx *sql.Tx, row Row, actor string) (PendingMove, error)
}

// reasonUpstreamEmpty is the reason string emitted when the
// empty-upstream safety guard trips.
const reasonUpstreamEmpty = "upstream_empty"

// reasonThresholdExceeded is the v1.7 reason string emitted when the
// percent-threshold guard blocks a drift run that would
// purge more than thresholdPct of local rows. The caller surfaces
// this via sync_jobs.summary.drift_blocked + an admin-confirm
// override flow that re-triggers the sync with force=true.
const reasonThresholdExceeded = "threshold_exceeded"

// sampleLimit caps the filename sample.
const sampleLimit = 20

// Run executes drift detection + purge for one repo. See package doc
// for invariants. actor is the users.login of the caller (system
// sync leaves this empty).
//
// thresholdPct controls the v1.7 percent-threshold safety guard.
// When > 0 and force is false, a drift count exceeding
// thresholdPct of local-row count is BLOCKED — the report is set with
// Skipped=true, Reason="threshold_exceeded", BlockedCount=len(drift),
// and adapter.Purge is NOT invoked. thresholdPct == 0 disables the
// guard entirely. force=true bypasses the guard regardless of
// thresholdPct (operator-confirmed override path).
//
// The guard is the SECOND-line check: the upstream-empty guard
// runs first and short-circuits before drift is computed, and
// the per-mirror drift_purge=false default still gates whether Run
// is called at all. With this guard in place, an operator who
// enabled drift_purge AND has a misconfigured upstream still gets
// one more chance to notice before the mirror is wiped.
func Run(
	ctx context.Context,
	tx *sql.Tx,
	repoID int64,
	actor string,
	adapter DriftAdapter,
	thresholdPct int,
	force bool,
) (DriftReport, []PendingMove, error) {
	report := DriftReport{Protocol: adapter.Protocol()}

	// 1. Collect upstream keys into a set (O(N) scan).
	upstream := adapter.UpstreamKeys()
	upstreamSet := make(map[Key]struct{}, len(upstream))
	for _, k := range upstream {
		upstreamSet[k] = struct{}{}
	}

	// 2. Load local rows.
	local, err := adapter.LocalRows(ctx, tx, repoID)
	if err != nil {
		return report, nil, fmt.Errorf("driftpurge: load local rows (proto=%s repo=%d): %w",
			adapter.Protocol(), repoID, err)
	}
	report.LocalCount = len(local)

	// 3. Empty-upstream guard. Zero upstream AND >0 local is
	//    dangerous: a misparsed feed must never wipe a live mirror.
	//    Zero upstream AND zero local is benign (no-op — e.g. a
	//    fresh mirror with a now-empty upstream).
	if len(upstream) == 0 && len(local) > 0 {
		report.Skipped = true
		report.Reason = reasonUpstreamEmpty
		return report, nil, nil
	}

	// 4. Compute drift = local \ upstream.
	drift := make([]Row, 0)
	for _, row := range local {
		if _, ok := upstreamSet[row.Key()]; ok {
			continue
		}
		drift = append(drift, row)
	}

	// 5. Sort drift lex-by-SampleFilename. Stable not needed:
	//    adapter filenames are unique per repo.
	sort.Slice(drift, func(i, j int) bool {
		return drift[i].SampleFilename() < drift[j].SampleFilename()
	})

	// 6. Populate Sample with first-20 filenames. Done
	//    BEFORE Purge loop so partial-failure paths still carry
	//    the intended sample.
	limit := len(drift)
	if limit > sampleLimit {
		limit = sampleLimit
	}
	report.Sample = make([]string, 0, limit)
	for i := 0; i < limit; i++ {
		report.Sample = append(report.Sample, drift[i].SampleFilename())
	}

	// 7. Percent-threshold guard. Compares drift count
	//    against thresholdPct of local-row count using integer
	//    cross-multiplication so the check is exact at any scale
	//    (no float division, no rounding ambiguity). force=true
	//    bypasses the guard for operator-confirmed overrides;
	//    thresholdPct==0 disables the guard entirely.
	//
	//    int64 cast on the multiplication keeps the check correct
	//    on 32-bit builds where `int` is 32 bits — without it,
	//    len(drift)*100 silently overflows above ~21M rows.
	//    OmniRepo only ships amd64+arm64 today (both 64-bit) but
	//    the cast is free.
	driftN := int64(len(drift))
	localN := int64(len(local))
	threshN := int64(thresholdPct)
	if threshN > 0 && !force && localN > 0 && driftN*100 > threshN*localN {
		report.Skipped = true
		report.Reason = reasonThresholdExceeded
		report.BlockedCount = len(drift)
		return report, nil, nil
	}

	// 8. Iterate drift and DELETE each row inside the caller's tx, collecting
	//    the corresponding file relocations as deferred PendingMoves. The
	//    actual trash moves are NOT performed here: they are irreversible
	//    (os.Rename) and must run only AFTER the caller's WriteTx commits, so
	//    a rollback (e.g. a later row's DELETE failing) can never strand an
	//    earlier restored row against an already-trashed file. The caller
	//    runs ApplyPendingMoves on the returned slice post-commit. Stop on
	//    first error — the caller's tx decides rollback vs partial-commit.
	pending := make([]PendingMove, 0, len(drift))
	for _, row := range drift {
		move, err := adapter.Purge(ctx, tx, row, actor)
		if err != nil {
			return report, nil, fmt.Errorf("driftpurge: purge %v (proto=%s repo=%d, %d of %d purged): %w",
				row.Key(), adapter.Protocol(), repoID, report.PurgedCount, len(drift), err)
		}
		pending = append(pending, move)
		report.PurgedCount++
	}

	return report, pending, nil
}

// PendingMove is a deferred trash relocation produced by purgeRow. The DELETE
// has already happened inside the caller's tx; the file move is held back so
// it runs only after that tx commits (see ApplyPendingMoves). Fields are
// unexported — callers obtain these from Run and hand them straight back to
// ApplyPendingMoves; they cannot (and need not) construct or inspect them.
type PendingMove struct {
	trash     storage.Trash
	label     string
	trashKind string
	id        int64
	path      string
	actor     string
	snapshot  []byte
}

// purgeRow is the shared tail of every adapter's Purge: marshal the snapshot
// sidecar and DELETE the metadata row inside the caller's tx. It returns the
// on-disk relocation as a PendingMove rather than performing it, because the
// move is irreversible (os.Rename) and must run only after the caller's
// WriteTx commits — otherwise a later row's failure rolling back the tx would
// leave earlier restored rows pointing at already-trashed files. A marshal or
// DELETE failure aborts the row (and the caller's tx) with nothing moved.
func purgeRow(ctx context.Context, tx *sql.Tx, trash storage.Trash, label, deleteSQL, trashKind string, id int64, snap map[string]any, path, actor string) (PendingMove, error) {
	snapBytes, err := json.Marshal(snap)
	if err != nil {
		return PendingMove{}, fmt.Errorf("%s: marshal snapshot id=%d: %w", label, id, err)
	}
	if _, err := tx.ExecContext(ctx, deleteSQL, id); err != nil {
		return PendingMove{}, fmt.Errorf("%s: delete id=%d: %w", label, id, err)
	}
	return PendingMove{
		trash:     trash,
		label:     label,
		trashKind: trashKind,
		id:        id,
		path:      path,
		actor:     actor,
		snapshot:  snapBytes,
	}, nil
}

// ApplyPendingMoves performs the deferred trash relocations returned by Run,
// AFTER the caller's purge tx has committed. It is best-effort and order-
// independent: every move is attempted even if an earlier one fails, because
// each row is already deleted (committed) and the moves are independent. A
// missing source file (os.ErrNotExist) is tolerated — the DELETE is the
// truth-of-record and the snapshot sidecar still lets an admin restore. A
// genuine move failure leaves an orphaned file (the drifted artifact stays on
// disk with no row); that is a recoverable storage leak, never a restored row
// pointing at a missing file. The first such error is returned (with the count
// of failures) so the caller can log it; callers treat drift purge as
// best-effort and do not fail the sync on it.
func ApplyPendingMoves(ctx context.Context, moves []PendingMove) error {
	var firstErr error
	failures := 0
	for _, m := range moves {
		if _, err := m.trash.MoveWithSnapshot(ctx, m.path, m.trashKind, m.id, m.actor, m.snapshot); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			failures++
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: trash move id=%d path=%q: %w", m.label, m.id, m.path, err)
			}
		}
	}
	if firstErr != nil {
		return fmt.Errorf("driftpurge: %d of %d trash move(s) failed; first: %w", failures, len(moves), firstErr)
	}
	return nil
}
