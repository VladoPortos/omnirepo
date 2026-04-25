package api

import (
	"net/http"

	"github.com/dxc-internal/omnirepo/internal/audit"
	"github.com/dxc-internal/omnirepo/internal/auth"
)

// recordAuditAs is the centralized "record an audit event for this actor"
// path for every state-changing project-scoped handler. It wraps SetActor
// + recordAudit so call sites stop hand-rolling the
// `uid := actor.ID; ActorUserID: &uid` pattern that introduced audit
// finding #7 (project-owned API keys silently writing actor_user_id = 0,
// violating the FK against users(id) and dropping the audit row).
//
// Plan 03-02 migrates every documented call site to this helper
// (REQUIREMENTS.md AUDITATTR-05).
//
// Authentication-layer call sites (admin_phase1.go handleLogin /
// handleLogout / handleChangePassword, lines 412-455) are intentionally
// NOT migrated to this helper: they are user-session-only by definition,
// the existing `ActorUserID: &uid` is correct, and they sometimes record
// events for actors who are not yet (or no longer) attached to the
// request context. Future maintainers should resist sweeping them
// mechanically (CONTEXT.md D-06).
func (d Deps) recordAuditAs(r *http.Request, e audit.Event, actor auth.Actor) {
	SetActor(&e, actor)
	d.recordAudit(r, e)
}

// RecordAuditAsForTest exposes recordAuditAs to the api_test package.
// Production callers must use the unexported method.
//
// Mirrors the InterceptPutObjectForTest pattern from Plan 02-03: tests
// reach package-private behavior through narrow `*ForTest` exports
// rather than promoting it to the production API surface.
func (d Deps) RecordAuditAsForTest(r *http.Request, e audit.Event, actor auth.Actor) {
	d.recordAuditAs(r, e, actor)
}

// RecordAuditForTest exposes recordAudit to the api_test package so tests
// can drive the slog.WarnContext error path on Audit.Record failure.
func (d Deps) RecordAuditForTest(r *http.Request, e audit.Event) {
	d.recordAudit(r, e)
}
