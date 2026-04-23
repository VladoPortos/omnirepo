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
	EvtScanPrune       EventKind = "scan.prune"

	// Phase 05-03 / SCAN-09 — admin-triggered Trivy DB rotation. Emitted
	// by the upload (operator-supplied tarball) and pull (online fetch)
	// admin endpoints after SwapDir completes. Details_json carries the
	// source ("uploaded" | "online-pulled"), size_bytes, and trivy_db_meta
	// id so downstream alerting can correlate the rotation with any scan
	// failures that follow.
	EvtTrivyDBRotated EventKind = "trivy.db.rotated"

	// Phase 2 Plan 12 — admin-triggered GC (D-37, D-38, OPS-06, SCAN-12).
	// gc.triggered is emitted by the REST endpoint at enqueue time
	// (super-admin actor recorded). gc.run is emitted by the GC sync-pool
	// handler after the run completes; details_json carries the GCReport
	// summary {blobs_deleted, bytes_freed, trash_entries_deleted,
	// sessions_pruned}.
	EvtGCTriggered EventKind = "gc.triggered"
	EvtGCRun       EventKind = "gc.run"

	// Phase 3 Plan 01 — package-repo protocols (RPM/APT/PyPI/Helm) and
	// their signing keys + metadata regen. Constants are declared here so
	// downstream plans (03-02..03-07) have a single enumeration point;
	// the concrete emissions happen in those plans' handlers and sync-job
	// runners. TestEveryStateChangingActionEmitsEvent covers the emit path.
	EvtSigningKeyCreated   EventKind = "signing_key.created"
	EvtSigningKeyRotated   EventKind = "signing_key.rotated"
	EvtSigningKeyUsed      EventKind = "signing_key.used"
	EvtRPMUpload           EventKind = "rpm.upload"
	EvtRPMDelete           EventKind = "rpm.delete"
	EvtDEBUpload           EventKind = "deb.upload"
	EvtDEBDelete           EventKind = "deb.delete"
	EvtPyPIUpload          EventKind = "pypi.upload"
	EvtPyPIDelete          EventKind = "pypi.delete"
	EvtHelmUpload          EventKind = "helm.upload"
	EvtHelmDelete          EventKind = "helm.delete"
	EvtRepoMetadataRegen   EventKind = "repo.metadata.regen"
	EvtRepoMetadataFailed  EventKind = "repo.metadata.failed"

	// Phase 3 Plan 06 — SYNC-05 sync-from-external (D-18). One per-job
	// lifecycle event each, plus a coarse heartbeat (every 50 files). The
	// REST enqueue endpoint emits EvtSyncStarted at the moment the
	// sync_jobs row is inserted; the worker emits EvtUpstreamCredUsed
	// (reuse of the existing event) at first upstream contact when
	// cred_id != null (D-19); progress + finished/failed are worker-side.
	EvtSyncStarted  EventKind = "sync.started"
	EvtSyncProgress EventKind = "sync.progress"
	EvtSyncFinished EventKind = "sync.finished"
	EvtSyncFailed   EventKind = "sync.failed"

	// Phase 4 Plan 05 — S3 access-key management (D-02, T-04-05-06).
	// "create" carries {project, id, label, access_key_id} — never the
	// plaintext secret. "revoke" carries {project, id, access_key_id}.
	EvtS3AccessKeyCreated EventKind = "s3.access-key.create"
	EvtS3AccessKeyRevoked EventKind = "s3.access-key.revoke"

	// Project-scoped API keys (D-1). Mints "omr_p_*" tokens that pipelines
	// can use to publish under one project. "create" carries {project, id,
	// name, prefix} — never the plaintext secret. "revoke" carries
	// {project, id, name}.
	EvtProjectAPIKeyCreated EventKind = "project.api-key.create"
	EvtProjectAPIKeyRevoked EventKind = "project.api-key.revoke"

	// User-scoped API keys (D-1). Mints "omr_u_*" tokens tied to a single
	// user (self-service, shown once). "create" carries {id, name, prefix};
	// "revoke" carries {id, name}. target_kind="user_api_key" so operator
	// can grep audit_log for per-key timelines without needing to know the
	// numeric id up front.
	EvtUserAPIKeyCreated EventKind = "user.api-key.create"
	EvtUserAPIKeyRevoked EventKind = "user.api-key.revoke"

	// S3 bucket provisioning (walkthrough 2026-04-17). Emitted by the
	// REST endpoint that creates/deletes an s3_buckets row + on-disk dir.
	// Details: {project, name}. Delete additionally carries
	// size_bytes_at_delete so post-mortem work can reason about drops.
	EvtS3BucketCreated EventKind = "s3.bucket.create"
	EvtS3BucketDeleted EventKind = "s3.bucket.delete"

	// Phase 4 Plan 10 — Git refs walker (D-37). Emitted by the
	// post-ReceivePack hook after a successful git_refs sync.
	// Details: {repo_id, ref_count, project}.
	EvtGitRefsSynced EventKind = "git.refs.synced"

	// Phase 10 — DB integrity_check (DBHEALTH-05, DBHEALTH-06). Emitted
	// by internal/metadata/pragmas.go (source=boot) and internal/api/
	// admin_db_health.go (source=manual).
	EvtIntegrityCheckTriggered EventKind = "admin.integrity_check.triggered"
	EvtIntegrityCheckCompleted EventKind = "admin.integrity_check.completed"
	EvtIntegrityCheckFailed    EventKind = "admin.integrity_check.failed"

	// Phase 11 — Mirror infrastructure widening (OCIHELM-04, GITMIRROR-08).
	//
	// EvtOciTagRebound: emitted by internal/protocol/helm/sync_handler.go
	// on (repo_id, name, version) collision where the new manifest digest
	// differs from the stored one. details_json per D-05:
	//   {name, version, old_digest, new_digest, upstream_url, repo_id,
	//    replaced_at}
	// The prior chart's on-disk path is soft-deleted via Trash.Move with
	// retention_label "oci_tag_rebound" (distinct from the generic
	// mirror-replaced label; enables operator-grep for CVE-driven
	// republication timelines). Production emission lands in plan 11-03.
	//
	// EvtMirrorSyncLFSDetected: emitted by internal/protocol/git/
	// sync_handler.go when a post-fetch tree walk finds .gitattributes
	// containing "filter=lfs" (D-08). Audit-only — no UI badge in v1.4.
	// details_json: {repo_id, project, sample_paths}. Production
	// emission lands in plan 11-06.
	EvtOciTagRebound         EventKind = "mirror.oci.tag_rebound"
	EvtMirrorSyncLFSDetected EventKind = "mirror.sync.lfs_detected"
)
