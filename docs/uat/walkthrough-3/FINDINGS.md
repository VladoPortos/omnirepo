# Walkthrough #3 — Consolidated findings

All findings across all batches. Source of truth for release gate.

Severity: **B** blocker · **R** real-bug · **m** minor · **n** noise.
Status: 🟨 Open · ✅ Closed · 🟥 Rejected (disputed)

| ID | Sev | Area | Title | Fix commit | Codex | Retest | Status |
|----|-----|------|-------|-----------|-------|--------|--------|
| F-01.1 | R | httperr.Write | All 4xx logged at slog ERROR — floods alerting + trips backend-log gate | `0f2dfd1` | ✅ Clean | ✅ Passed | ✅ Closed |
| F-01.2 | n | browser console | Browser-native "Failed to load resource" logs on every 4xx fetch | _(no fix — cure worse than disease)_ | — | ✅ Accepted | ✅ Closed |
| F-01.3 | R | AuthGuard + LoginPage | Deep-link lost across login redirect | `12ac7e1` | ✅ Clean | ✅ Passed | ✅ Closed |
| F-01.4 | m | openapi.yaml | `/setup/status` + `/setup/superadmin` undocumented | `3d06d11` | ✅ Clean | ✅ Passed | ✅ Closed |
| F-01.5 | R | auth pages | Cards render squished — motion.div flex-item has no width | `fa179e9` | ⬜ Pending | ✅ Passed | ✅ Closed |
| F-02.1 | R | main.tsx | Toaster never mounted — all `toast.*` silent across the app | `bdca441` | ✅ Clean | ✅ Passed | ✅ Closed |
| F-02.2 | m | handleChangePassword | Wrong-current-password on self-service change not audited | `ddc6d81` | ✅ Clean | ✅ Passed | ✅ Closed |
| F-02.3 | **B** | handleDeleteUser | Self-delete + last-super-admin delete both succeed → instance soft-brick | `7c8daea` + `88caa0c` | ✅ Clean | ✅ Passed | ✅ Closed |

---

## Detail

### F-01.1 All error responses logged at slog ERROR level, regardless of HTTP status
- **Severity:** R / real-bug
- **Area:** `internal/httperr/write.go` (Write) — affects every handler funneling through `writeJSONError`
- **Symptom:** Every 4xx (validation / 401 / 403 / 404) emitted `level=ERROR msg=api.error ...`, polluting alert streams and tripping the testing-protocol §4 backend-log gate on every normal rejection.
- **Repro:**
  1. Start server clean.
  2. `curl -X POST http://localhost:18080/api/v1/setup/superadmin -d '{"login":"x","email":"x@y","password":"123"}'` → 422.
  3. `grep ERROR $OMNIREPO_DATA_ROOT/server.log` → one hit for a perfectly normal validation response.
- **Root cause:** `internal/httperr/write.go:51` unconditional `slog.ErrorContext(...)` ignoring class/status.
- **Fix:** commit `0f2dfd1` — level routes by status: 5xx or `operator_action_required` → ERROR, 4xx → WARN. Added `status` field in structured log. `TestWrite_LogLevelByStatus` pins the rule.
- **Codex verify:** ✅ Clean (batched)
- **Retest:** ✅ Re-triggered 422; log now `WARN ... status=422 class=validation`; grep-ERROR returns 0 hits.
- **Status:** ✅ Closed

### F-01.2 Browser-native "Failed to load resource" console entries on 4xx fetch (noise)
- **Severity:** n / noise
- **Area:** LoginPage + any UI that calls `fetch()` against an endpoint that may legitimately return 4xx
- **Symptom:** `ERROR  Failed to load resource: the server responded with a status of 401/404` surfaces in the browser console on wrong-password login / repo-not-found page / etc.
- **Root cause:** Chromium emits this for every non-2xx fetch; it's a platform log, not a JS error. The app handles both 401 and 404 correctly.
- **Fix:** None proposed. Any workaround (200-with-failure-body, XHR-with-custom-plumbing, `console.error` shim) would mask real bugs. Console-cleanliness gate in the testing protocol should be interpreted as "no JS-app errors" — browser-native network logs are exempt.
- **Status:** ✅ Closed as accepted noise

### F-01.3 Deep-link lost across login redirect
- **Severity:** R / real-bug
- **Area:** `web/src/App.tsx` (AuthGuard), `web/src/pages/LoginPage.tsx`
- **Symptom:** Visiting e.g. `/projects/acme/docker/demo` while logged out → /login → after sign-in lands on `/`, not the original path.
- **Root cause:** AuthGuard's `<Navigate to="/login" replace />` didn't pass `state={{ from: location }}`; LoginPage hardcoded `navigate('/')` on success.
- **Fix:** commit `12ac7e1` — AuthGuard attaches `state.from`; LoginPage navigates to `state.from.pathname` (+ search + hash) on success. Path must start with `/` and not `//` (open-redirect guard); otherwise falls back to `/`.
- **Codex verify:** ✅ Clean (batched)
- **Retest:** ✅ Retried repro — lands on `/projects/acme/docker/demo` (NotFound page, correct for now).
- **Status:** ✅ Closed

### F-02.3 Self-delete + last-super-admin delete both succeed (BLOCKER)
- **Severity:** B / blocker
- **Area:** `internal/api/admin_phase1.go:565` `handleDeleteUser`
- **Symptom:** Super-admin deleted itself via the admin API; zero live super-admins remained. Instance loses all admin-surface access; recovery needs direct SQL on the data volume.
- **Fix:** commit `7c8daea` — two safety checks in `handleDeleteUser`: actor ≠ target (else 409, point user to `/me`); if target is super-admin, require `CountLiveSuperAdmins > 1` (else 409 "promote another user first"). New `Users.CountLiveSuperAdmins` helper. Regression tests pin both rules.
- **Retest:** ✅ Post-fix `DELETE /admin/users/superadmin` as superadmin → 409 with correct envelope; row stays live.
- **Status:** ✅ Closed

### F-02.2 Wrong-current-password on self-service change not audited
- **Severity:** m / minor (observability gap)
- **Area:** `internal/api/admin_phase1.go:419` `handleChangePassword`
- **Symptom:** Failing `/auth/change-password` (wrong current password) returned 401 but emitted no audit row. Same threat surface as login brute-force, but untracked.
- **Fix:** commit `ddc6d81` — failure branch now writes `auth.password.changed / outcome=wrong_password`; success branch now sets `outcome=ok` explicitly.
- **Retest:** ✅ `auth_log` carries both outcomes after retrying the repro.
- **Status:** ✅ Closed

### F-02.1 Toast host never mounted — every `toast.*` call silent
- **Severity:** R / real-bug (affects the whole app)
- **Area:** `web/src/main.tsx` (app root)
- **Symptom:** `<Toaster>` component defined in `components/ui/sonner.tsx` but never mounted anywhere. All `toast.success` / `toast.error` calls across UsersPage, TLSPage, DockerRepoPage, MaintenancePage, TrashPage, GCPage, PypiRepoPage were silent no-ops. Observed in Batch 02 case 2.4 (dup-create → form clears, zero feedback).
- **Fix:** commit `bdca441` — `main.tsx` now mounts `<Toaster richColors position="top-right" />` as a sibling of RouterProvider. DOM probe after fix confirms a toast element with text `login exists` appears on dup-create within the 50ms poll.
- **Retest:** ✅ Confirmed.
- **Status:** ✅ Closed

### F-01.5 Auth cards render squished — motion.div flex-item has no width
- **Severity:** R / real-bug
- **Area:** `web/src/pages/LoginPage.tsx`, `web/src/pages/SetupPage.tsx` (both branches), `web/src/pages/ChangePasswordPage.tsx`
- **Symptom:** Every unauthenticated card (login, setup form, "Setup complete", change-password) renders at ~137-272px instead of the intended `max-w-md` (448px). "Setup complete" title wraps to two lines; description wraps to four.
- **Root cause:** `motion.div` wraps the `Card` inside a `flex items-center justify-center` parent. With no width class on the motion.div and no intrinsic-min from the content, the flex item shrinks below `max-w-md` and the Card's `w-full` then resolves against the collapsed parent.
- **Fix:** Add `className="w-full max-w-md"` to every `motion.div` wrapper. Pinned four call sites. Card width measured at 448px after fix.
- **Retest:** ✅ Screenshots show the card at proper width on /setup (setup-complete) and /login.
- **Status:** ✅ Closed

### F-01.4 Setup endpoints undocumented in OpenAPI spec
- **Severity:** m / minor
- **Area:** `internal/api/openapi.yaml`
- **Symptom:** `/setup/status` + `/setup/superadmin` missing from the spec served at `/api/v1/openapi.yaml`. Swagger UI and generated clients never surface the bootstrap flow — one of the first endpoints a new operator needs.
- **Root cause:** Spec drift — handlers shipped without corresponding spec entries.
- **Fix:** commit `3d06d11` — added schemas + paths; types regenerated; hand-written Go types now alias the generated names; email field kept plain `string` so handler's non-empty check stays authoritative.
- **Retest:** ✅ Both paths present; full Go tests green.
- **Status:** ✅ Closed
