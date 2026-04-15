// Package audit is the dual-sink audit logger (D-33/D-34/D-35): every call
// to Record writes one row into the audit_log table AND one JSON line to a
// size-rotated NDJSON file under /var/lib/omnirepo/logs/audit.log.
//
// OQ-9 semantics: the DB insert is strict (errors bubble to the caller); the
// NDJSON mirror is best-effort (failure is slog.Warn, not returned) so a
// transient fs error on the log file can never make a state-changing action
// look like it failed.
package audit

// EventKind enumerates the audit event kinds emitted during Phase 1.
// Phase 2+ extends this list; the set below is the complete Phase 1 roster.
// Downstream test TestEveryStateChangingActionEmitsEvent iterates every
// constant and asserts emission, so new phases that add kinds MUST also
// extend that test.
type EventKind string

const (
	EvtAuthLoginSuccess    EventKind = "auth.login.success"
	EvtAuthLoginFailure    EventKind = "auth.login.failure"
	EvtAuthLogout          EventKind = "auth.logout"
	EvtAuthPasswordChanged EventKind = "auth.password.changed"
	EvtUserCreated         EventKind = "user.created"
	EvtUserUpdated         EventKind = "user.updated"
	EvtUserDeleted         EventKind = "user.deleted"
	EvtProjectCreated      EventKind = "project.created"
	EvtProjectUpdated      EventKind = "project.updated"
	EvtProjectDeleted      EventKind = "project.deleted"
	EvtMemberAdded         EventKind = "member.added"
	EvtMemberRemoved       EventKind = "member.removed"
	EvtRepoCreated         EventKind = "repo.created"
	EvtRepoUpdated         EventKind = "repo.updated"
	EvtRepoDeleted         EventKind = "repo.deleted"
	EvtRepoWiped           EventKind = "repo.wiped"
	EvtTLSCertUploaded     EventKind = "tls.cert.uploaded"
	EvtBootstrapApplied    EventKind = "bootstrap.applied"
	EvtMaintenanceToggled  EventKind = "maintenance.toggled"

	// Phase 2 Plan 02 — upstream credentials (D-13). "used" is emitted by
	// Phase 02-10 pull-external at the consumer side; declared here for a
	// single enumeration point.
	EvtUpstreamCredCreated EventKind = "upstream_cred.created"
	EvtUpstreamCredUpdated EventKind = "upstream_cred.updated"
	EvtUpstreamCredDeleted EventKind = "upstream_cred.deleted"
	EvtUpstreamCredUsed    EventKind = "upstream_cred.used"

	// Phase 2 Plan 08 — RAW handler (D-27..D-31). raw.put / raw.delete are
	// emitted by the handler in this plan; raw.get.blocked is reserved for
	// the severity gate hook that 02-09 will wire in (declared here for a
	// single enumeration point).
	EvtRawPut        EventKind = "raw.put"
	EvtRawDelete     EventKind = "raw.delete"
	EvtRawGetBlocked EventKind = "raw.get.blocked"

	// Phase 2 Plan 06 — OCI blob state-changing actions (D-03).
	// Emitted by the /v2/<name>/blobs/... handlers as best-effort
	// records alongside the writer tx that mutates docker_blobs /
	// blob_upload_sessions / blob_uploads (audit.Record is invoked
	// outside the tx so a transient NDJSON failure never masks a
	// successful upload).
	EvtOCIBlobUploaded EventKind = "oci.blob.uploaded"
	EvtOCIBlobMounted  EventKind = "oci.blob.mounted"
	EvtOCIBlobDeleted  EventKind = "oci.blob.deleted"

	// Phase 2 Plan 07 — OCI manifest + tag state-changing actions.
	// Emitted by /v2/<name>/manifests/<ref> PUT/DELETE and /v2/<name>/tags/<tag>
	// DELETE. Best-effort after the writer tx commits.
	EvtOCIManifestUploaded EventKind = "oci.manifest.uploaded"
	EvtOCIManifestDeleted  EventKind = "oci.manifest.deleted"
	EvtOCITagDeleted       EventKind = "oci.tag.deleted"

	// Phase 2 Plan 10 — OCI pull-external + promote (D-04, D-05, D-12, D-13).
	// pull_external.started/finished/failed are emitted by the pull-external
	// sync-job handler and its REST enqueue endpoint. oci.promote is emitted
	// by the promote REST handler after the zero-blob-copy retag tx commits.
	EvtOCIPullExternalStarted  EventKind = "oci.pull_external.started"
	EvtOCIPullExternalFinished EventKind = "oci.pull_external.finished"
	EvtOCIPullExternalFailed   EventKind = "oci.pull_external.failed"
	EvtOCIPromote              EventKind = "oci.promote"

	// Phase 2 Plan 09 — scan pipeline (D-23..D-26, SCAN-03..08).
	// Emitted by the scan handler (started/finished/failed) and by the
	// severity gate middleware (gate.blocked) on a 403 deny. Best-effort:
	// audit failure never masks scan completion.
	EvtScanStarted     EventKind = "scan.started"
	EvtScanFinished    EventKind = "scan.finished"
	EvtScanFailed      EventKind = "scan.failed"
	EvtScanGateBlocked EventKind = "scan.gate.blocked"
)
