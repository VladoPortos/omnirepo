// Package audit is the dual-sink audit logger: every call to Record writes
// one row into the audit_log table AND one JSON line to a size-rotated NDJSON
// file under /var/lib/omnirepo/logs/audit.log.
//
// The DB insert is strict (errors bubble to the caller); the NDJSON mirror is
// best-effort (failure is slog.Warn, not returned) so a transient fs error on
// the log file can never make a state-changing action look like it failed.
package audit

// EventKind enumerates the audit event kinds emitted by the server.
// Downstream test TestEveryStateChangingActionEmitsEvent iterates every
// constant and asserts emission, so additions that add kinds MUST also
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
	EvtTLSCertUploadFailed EventKind = "tls.cert.upload.failed"
	EvtBootstrapApplied    EventKind = "bootstrap.applied"
	EvtMaintenanceToggled  EventKind = "maintenance.toggled"

	// Upstream credentials. "used" is emitted by pull-external at the
	// consumer side; declared here for a single enumeration point.
	EvtUpstreamCredCreated EventKind = "upstream_cred.created"
	EvtUpstreamCredUpdated EventKind = "upstream_cred.updated"
	EvtUpstreamCredDeleted EventKind = "upstream_cred.deleted"
	EvtUpstreamCredUsed    EventKind = "upstream_cred.used"

	// RAW handler. raw.put / raw.delete are emitted by the handler;
	// raw.get.blocked is reserved for the severity gate hook (declared
	// here for a single enumeration point).
	EvtRawPut        EventKind = "raw.put"
	EvtRawDelete     EventKind = "raw.delete"
	EvtRawGetBlocked EventKind = "raw.get.blocked"

	// OCI blob state-changing actions.
	// Emitted by the /v2/<name>/blobs/... handlers as best-effort
	// records alongside the writer tx that mutates docker_blobs /
	// blob_upload_sessions / blob_uploads (audit.Record is invoked
	// outside the tx so a transient NDJSON failure never masks a
	// successful upload).
	EvtOCIBlobUploaded EventKind = "oci.blob.uploaded"
	EvtOCIBlobMounted  EventKind = "oci.blob.mounted"
	EvtOCIBlobDeleted  EventKind = "oci.blob.deleted"

	// OCI manifest + tag state-changing actions.
	// Emitted by /v2/<name>/manifests/<ref> PUT/DELETE and /v2/<name>/tags/<tag>
	// DELETE. Best-effort after the writer tx commits.
	EvtOCIManifestUploaded EventKind = "oci.manifest.uploaded"
	EvtOCIManifestDeleted  EventKind = "oci.manifest.deleted"
	EvtOCITagDeleted       EventKind = "oci.tag.deleted"

	// OCI pull-external + promote.
	// pull_external.started/finished/failed are emitted by the pull-external
	// sync-job handler and its REST enqueue endpoint. oci.promote is emitted
	// by the promote REST handler after the zero-blob-copy retag tx commits.
	EvtOCIPullExternalStarted  EventKind = "oci.pull_external.started"
	EvtOCIPullExternalFinished EventKind = "oci.pull_external.finished"
	EvtOCIPullExternalFailed   EventKind = "oci.pull_external.failed"
	EvtOCIPromote              EventKind = "oci.promote"

	// Scan pipeline.
	// Emitted by the scan handler (started/finished/failed) and by the
	// severity gate middleware (gate.blocked) on a 403 deny. Best-effort:
	// audit failure never masks scan completion.
	EvtScanStarted     EventKind = "scan.started"
	EvtScanFinished    EventKind = "scan.finished"
	EvtScanFailed      EventKind = "scan.failed"
	EvtScanGateBlocked EventKind = "scan.gate.blocked"
	EvtScanPrune       EventKind = "scan.prune"

	// Admin-triggered Trivy DB rotation. Emitted by the upload
	// (operator-supplied tarball) and pull (online fetch) admin endpoints
	// after SwapDir completes. Details_json carries the source
	// ("uploaded" | "online-pulled"), size_bytes, and trivy_db_meta id so
	// downstream alerting can correlate the rotation with any scan failures
	// that follow.
	EvtTrivyDBRotated EventKind = "trivy.db.rotated"

	// Admin-triggered GC.
	// gc.triggered is emitted by the REST endpoint at enqueue time
	// (super-admin actor recorded). gc.run is emitted by the GC sync-pool
	// handler after the run completes; details_json carries the GCReport
	// summary {blobs_deleted, bytes_freed, trash_entries_deleted,
	// sessions_pruned}.
	EvtGCTriggered EventKind = "gc.triggered"
	EvtGCRun       EventKind = "gc.run"

	// Package-repo protocols (RPM/APT/PyPI/Helm) and their signing keys +
	// metadata regen. Constants are declared here so the protocols have a
	// single enumeration point; the concrete emissions happen in their
	// handlers and sync-job runners. TestEveryStateChangingActionEmitsEvent
	// covers the emit path.
	EvtSigningKeyCreated  EventKind = "signing_key.created"
	EvtSigningKeyRotated  EventKind = "signing_key.rotated"
	EvtSigningKeyUsed     EventKind = "signing_key.used"
	EvtRPMUpload          EventKind = "rpm.upload"
	EvtRPMDelete          EventKind = "rpm.delete"
	EvtDEBUpload          EventKind = "deb.upload"
	EvtDEBDelete          EventKind = "deb.delete"
	EvtPyPIUpload         EventKind = "pypi.upload"
	EvtPyPIDelete         EventKind = "pypi.delete"
	EvtHelmUpload         EventKind = "helm.upload"
	EvtHelmDelete         EventKind = "helm.delete"
	EvtRepoMetadataRegen  EventKind = "repo.metadata.regen"
	EvtRepoMetadataFailed EventKind = "repo.metadata.failed"

	// Sync-from-external. One per-job lifecycle event each, plus a coarse
	// heartbeat (every 50 files). The REST enqueue endpoint emits
	// EvtSyncStarted at the moment the sync_jobs row is inserted; the worker
	// emits EvtUpstreamCredUsed (reuse of the existing event) at first
	// upstream contact when cred_id != null; progress + finished/failed are
	// worker-side.
	EvtSyncStarted  EventKind = "sync.started"
	EvtSyncProgress EventKind = "sync.progress"
	EvtSyncFinished EventKind = "sync.finished"
	EvtSyncFailed   EventKind = "sync.failed"

	// PyPI parser hardening.
	// Generic cross-protocol skip event emitted when the sync loop
	// rejects an upstream file before download. Ships two reason enum
	// values:
	//   - "pep440_invalid"  — filename's version slot failed
	//     pep440.Validate (sdist multi-candidate scan exhausted OR
	//     wheel positional parts[1] malformed)
	//   - "unsupported_ext" — filename has no .tar.gz/.tgz/.zip/.whl
	//     suffix (reserved; today these are filtered earlier by
	//     isInstallableExt without an audit row)
	// target_kind="repo", target_id=repo_id as decimal string.
	// details_json: {filename, reason, protocol, upstream_url, repo_id}.
	// Other protocols (drift purge, potential RPM/APT hardening) MAY emit
	// this same kind with a different protocol + reason; the event is
	// deliberately not PyPI-specific.
	EvtSyncFileSkipped EventKind = "sync.file_skipped"

	// S3 access-key management.
	// "create" carries {project, id, label, access_key_id} — never the
	// plaintext secret. "revoke" carries {project, id, access_key_id}.
	EvtS3AccessKeyCreated EventKind = "s3.access-key.create"
	EvtS3AccessKeyRevoked EventKind = "s3.access-key.revoke"

	// Project-scoped API keys. Mints "omr_p_*" tokens that pipelines
	// can use to publish under one project. "create" carries {project, id,
	// name, prefix} — never the plaintext secret. "revoke" carries
	// {project, id, name}.
	EvtProjectAPIKeyCreated EventKind = "project.api-key.create"
	EvtProjectAPIKeyRevoked EventKind = "project.api-key.revoke"

	// User-scoped API keys. Mints "omr_u_*" tokens tied to a single
	// user (self-service, shown once). "create" carries {id, name, prefix};
	// "revoke" carries {id, name}. target_kind="user_api_key" so operator
	// can grep audit_log for per-key timelines without needing to know the
	// numeric id up front.
	EvtUserAPIKeyCreated EventKind = "user.api-key.create"
	EvtUserAPIKeyRevoked EventKind = "user.api-key.revoke"

	// S3 bucket provisioning. Emitted by the REST endpoint that
	// creates/deletes an s3_buckets row + on-disk dir, and by the S3
	// protocol audit middleware for bucket-level PUT/DELETE through the
	// S3 API itself (details then carry source="s3-api").
	// Details: {project, name}. Delete additionally carries
	// size_bytes_at_delete so post-mortem work can reason about drops.
	EvtS3BucketCreated EventKind = "s3.bucket.create"
	EvtS3BucketDeleted EventKind = "s3.bucket.delete"

	// S3 object mutations through the S3 protocol surface. Emitted by the
	// protocol audit middleware (internal/protocol/s3/audit.go) after the
	// response completes; actor is the SigV4-resolved S3 access key.
	// Details: {bucket, key, status} (+batch=true for POST ?delete).
	// Outcome: ok | denied | failed (from response status).
	EvtS3ObjectPut    EventKind = "s3.object.put"
	EvtS3ObjectDelete EventKind = "s3.object.delete"

	// S3 multipart lifecycle endpoints that materialize or discard an
	// object: CompleteMultipartUpload (POST ?uploadId) and
	// AbortMultipartUpload (DELETE ?uploadId). Part uploads themselves are
	// deliberately not audited (high volume, no artifact-level meaning).
	EvtS3MultipartCompleted EventKind = "s3.multipart.complete"
	EvtS3MultipartAborted   EventKind = "s3.multipart.abort"

	// Git refs walker. Emitted by the post-ReceivePack hook after a
	// successful git_refs sync.
	// Details: {repo_id, ref_count, project}.
	EvtGitRefsSynced EventKind = "git.refs.synced"

	// Git fetch/clone. Emitted by the git audit middleware for every
	// completed upload-pack POST (the actual pack transfer; info/refs
	// advertisements are skipped so one clone logs one event).
	// Details: {repo_id, project, status, bytes}.
	EvtGitFetch EventKind = "git.fetch"

	// DB integrity_check. Emitted by internal/metadata/pragmas.go
	// (source=boot) and internal/api/admin_db_health.go (source=manual).
	EvtIntegrityCheckTriggered EventKind = "admin.integrity_check.triggered"
	EvtIntegrityCheckCompleted EventKind = "admin.integrity_check.completed"
	EvtIntegrityCheckFailed    EventKind = "admin.integrity_check.failed"

	// RBAC maintainer/viewer split.
	// Emitted after a successful PATCH /projects/{name}/members/{login}.
	// details_json: {"user": "<login>", "old_role": "...", "new_role": "..."}.
	// target_kind="project", target_id=<project_name> (matches member.added shape).
	EvtMemberRoleChanged EventKind = "member.role_changed"

	// Mirror infrastructure widening.
	//
	// EvtOciTagRebound: emitted by internal/protocol/helm/sync_handler.go
	// on (repo_id, name, version) collision where the new manifest digest
	// differs from the stored one. details_json:
	//   {name, version, old_digest, new_digest, upstream_url, repo_id,
	//    replaced_at}
	// The prior chart's on-disk path is soft-deleted via Trash.Move with
	// retention_label "oci_tag_rebound" (distinct from the generic
	// mirror-replaced label; enables operator-grep for CVE-driven
	// republication timelines).
	//
	// EvtMirrorSyncLFSDetected: emitted by internal/protocol/git/
	// sync_handler.go when a post-fetch tree walk finds .gitattributes
	// containing "filter=lfs". Audit-only — no UI badge.
	// details_json: {repo_id, project, sample_paths}.
	EvtOciTagRebound         EventKind = "mirror.oci.tag_rebound"
	EvtMirrorSyncLFSDetected EventKind = "mirror.sync.lfs_detected"

	// Drift purge.
	//
	// EvtMirrorDriftPurged: emitted by internal/protocol/*/sync_handler.go
	// after a successful drift diff with drift_count > 0. details_json:
	//
	//   {
	//     "protocol":     "pypi"|"rpm"|"deb"|"helm",
	//     "count":        int64,        // PurgedCount from driftpurge.DriftReport
	//     "sample":       []string,     // lex-sorted first-20 filenames
	//     "sync_job_id":  int64,
	//     "upstream_url": string
	//   }
	//
	// Zero-count emission is intentionally skipped. Run evidence for
	// zero-drift syncs lives in sync_jobs.summary.drift_purged (the handler
	// writes this integer key unconditionally when drift detection ran).
	EvtMirrorDriftPurged EventKind = "mirror.drift_purged"

	// EvtMirrorDriftPurgeSkipped: emitted when one of driftpurge.Run's
	// safety guards trips. Any misparsed/empty upstream response or
	// suspiciously large drift fraction that would otherwise wipe a
	// populated mirror triggers this event instead of a real purge.
	//
	// details_json (base shape, all reasons):
	//
	//   {
	//     "protocol":     "pypi"|"rpm"|"deb"|"helm",
	//     "reason":       "upstream_empty" | "threshold_exceeded",
	//     "local_count":  int64,   // LocalCount from DriftReport
	//     "sync_job_id":  int64,
	//     "upstream_url": string
	//   }
	//
	// reason="upstream_empty" — Upstream returned zero keys while local
	//   has rows; emitted shape uses the base fields only.
	//
	// reason="threshold_exceeded" — Drift count exceeded the configured
	//   percent threshold of local rows. Adds two extra fields beyond the
	//   base shape:
	//
	//     "blocked_count": int64,  // BlockedCount from DriftReport
	//     "threshold_pct": int64,  // cfg.Sync.DriftPurgeThresholdPct
	//
	//   External audit consumers must accept either reason value;
	//   hard-coding `reason=="upstream_empty"` will silently drop the
	//   threshold-blocked events.
	EvtMirrorDriftPurgeSkipped EventKind = "mirror.drift_purge_skipped"
)
