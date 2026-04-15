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
)
