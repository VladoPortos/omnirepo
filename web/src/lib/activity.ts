// activityTargetHref maps an audit event row to a navigable URL using
// the (action, target_id) shape the dashboard activity endpoint emits.
//
// Supported linkable shapes:
//   project.{created,updated}     target_id = "<slug>"                  → /projects/<slug>
//   member.{added,removed,...}    target_id = "<slug>"                  → /projects/<slug>
//   repo.*                        target_id = "<slug>/<type>/<name>"    → /projects/<slug>/<type>/<name>
//   signing_key.*                 same shape as repo.*
//
// Explicitly NOT linked (returns empty string):
//   project.deleted               — target slug no longer resolves to a live project page (F-2)
//   project.api-key.*             — target_id is the numeric api-key id, there is no
//                                   standalone api-key detail page, so we cannot drill
//                                   through at all (F-1)
//   auth.*, user.*, tls.*, trivy.*, maintenance.*, scan.*, pypi.*, upstream_cred.*
//                                 — no useful drill-through target
export function activityTargetHref(action: string, targetID: string): string {
  if (!targetID) return '';
  // project.api-key.* carries the key id, not the project slug; there is no
  // single-key view, and surfacing the project's Overview would surprise users
  // who expect the link text to match its destination. Skip.
  if (action.startsWith('project.api-key.')) return '';
  // project.deleted's target slug no longer resolves — don't hand users a 404.
  if (action === 'project.deleted') return '';

  const parts = targetID.split('/');
  if (action.startsWith('project.') || action.startsWith('member.')) {
    return parts.length >= 1 && parts[0] ? `/projects/${parts[0]}` : '';
  }
  if (action.startsWith('repo.') || action.startsWith('signing_key.')) {
    if (parts.length >= 3 && parts[0] && parts[1] && parts[2]) {
      return `/projects/${parts[0]}/${parts[1]}/${parts[2]}`;
    }
  }
  return '';
}
