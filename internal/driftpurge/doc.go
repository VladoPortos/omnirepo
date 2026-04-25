// Package driftpurge — see engine.go for the full package doc.
//
// Design invariants (do not break without updating CONTEXT.md):
//   - Engine imports zero protocol packages (layering seam).
//   - Adapters carry protocol knowledge; engine knows Key/Row/Report.
//   - Empty-upstream guard (D-08) is the only safety net in v1.5.
//   - Engine never emits audit events directly; caller translates
//     DriftReport into mirror.drift_purged / mirror.drift_purge_skipped.
//   - Engine never rolls back the tx; caller owns transaction lifecycle.
package driftpurge
