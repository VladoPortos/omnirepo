// Package driftpurge implements the protocol-agnostic drift-detection
// engine for mirror repos. v1.5 Phase 6 (DRIFTPURGE-01..05).
//
// The engine compares (parsed upstream entries) against (local DB rows)
// and invokes an adapter to soft-delete rows whose upstream key has
// vanished. The empty-upstream safety guard (D-08) prevents a
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
	"fmt"
	"sort"
)

// Key is the comparable identity tuple for a drift diff. Fields are
// stringified by the adapter so map[Key]struct{} lookups are O(1).
// Per-protocol key shapes per D-12:
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
	// mirror.drift_purged audit `sample` array (D-18). PyPI returns
	// filename; helm returns name-version.tgz; RPM/APT return NEVRA.
	SampleFilename() string
}

// DriftReport is the outcome of a single driftpurge.Run call.
//
// Skipped=true means the empty-upstream guard (D-08) tripped and
// adapter.Purge was NOT invoked. LocalCount carries the number of
// rows that WOULD have been vulnerable so the caller can emit
// mirror.drift_purge_skipped with diagnostics (D-20).
//
// PurgedCount is the number of rows successfully purged. On partial
// failure (adapter.Purge returned an error mid-iteration) PurgedCount
// reflects the rows that landed in trash before the error; callers
// decide whether to treat this as a full sync failure. The engine
// never rolls back — the caller owns the tx.
//
// Sample is lex-sorted first-20 (D-18) of drift filenames,
// populated on successful full-iteration runs (Skipped=false, err=nil).
// On partial failure it contains the sample computed BEFORE iteration
// started (so audit can still emit the intended sample).
type DriftReport struct {
	Protocol    string
	PurgedCount int
	Sample      []string
	Skipped     bool
	Reason      string
	LocalCount  int
}

// DriftAdapter is implemented by each per-protocol adapter (see
// pypi_adapter.go etc. in this package). It is the ONLY seam between
// the protocol-agnostic engine and protocol-specific row/key shape.
type DriftAdapter interface {
	// Protocol returns the short name ("pypi"|"rpm"|"deb"|"helm")
	// used in audit details_json (D-19/D-20).
	Protocol() string
	// TrashKind returns the Trash `kind` string ("pypi_file_drift"
	// etc. per D-03) used when the adapter's Purge writes a trash
	// holder via storage.Trash.MoveWithSnapshot.
	TrashKind() string
	// UpstreamKeys returns the parsed upstream entries' drift keys.
	// Called once per Run; result is converted to a set.
	UpstreamKeys() []Key
	// LocalRows loads every local row for repoID that participates
	// in drift. Called once per Run; ordering does not matter —
	// the engine sorts by SampleFilename for the Sample output.
	LocalRows(ctx context.Context, tx *sql.Tx, repoID int64) ([]Row, error)
	// Purge soft-deletes one row: writes a trash holder carrying
	// the row snapshot and DELETEs the row. Called once per drift
	// row; failures propagate up via Run's error return.
	Purge(ctx context.Context, tx *sql.Tx, row Row, actor string) error
}

// reasonUpstreamEmpty is the D-20 reason string emitted when the
// empty-upstream safety guard trips.
const reasonUpstreamEmpty = "upstream_empty"

// sampleLimit caps the filename sample per D-18.
const sampleLimit = 20

// Run executes drift detection + purge for one repo. See package doc
// for invariants. actor is the users.login of the caller (system
// sync leaves this empty).
func Run(
	ctx context.Context,
	tx *sql.Tx,
	repoID int64,
	actor string,
	adapter DriftAdapter,
) (DriftReport, error) {
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
		return report, fmt.Errorf("driftpurge: load local rows (proto=%s repo=%d): %w",
			adapter.Protocol(), repoID, err)
	}
	report.LocalCount = len(local)

	// 3. Empty-upstream guard (D-08). Zero upstream AND >0 local is
	//    dangerous: a misparsed feed must never wipe a live mirror.
	//    Zero upstream AND zero local is benign (no-op — e.g. a
	//    fresh mirror with a now-empty upstream).
	if len(upstream) == 0 && len(local) > 0 {
		report.Skipped = true
		report.Reason = reasonUpstreamEmpty
		return report, nil
	}

	// 4. Compute drift = local \ upstream.
	drift := make([]Row, 0)
	for _, row := range local {
		if _, ok := upstreamSet[row.Key()]; ok {
			continue
		}
		drift = append(drift, row)
	}

	// 5. Sort drift lex-by-SampleFilename (D-18). Stable not needed:
	//    adapter filenames are unique per repo.
	sort.Slice(drift, func(i, j int) bool {
		return drift[i].SampleFilename() < drift[j].SampleFilename()
	})

	// 6. Populate Sample with first-20 filenames (D-18). Done
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

	// 7. Iterate drift and purge each row. Stop on first error —
	//    the caller's tx decides rollback vs partial-commit.
	for _, row := range drift {
		if err := adapter.Purge(ctx, tx, row, actor); err != nil {
			return report, fmt.Errorf("driftpurge: purge %v (proto=%s repo=%d, %d of %d purged): %w",
				row.Key(), adapter.Protocol(), repoID, report.PurgedCount, len(drift), err)
		}
		report.PurgedCount++
	}

	return report, nil
}
