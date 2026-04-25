# Batch 14 — Admin: TLS · audit · S3 actor · trash · GC · maintenance · DB health

**Status:** ✅ Passed clean (0 findings; v1.7 S3AUDIT migration verified)
**Prereqs:** Batch 13 ✅

## Test cases

### 14.1 Audit Log UI ✅
- `/admin/audit` renders with sidebar Admin nav (Users · Audit Log · TLS Certificates · Trivy Database · Garbage Collection · Trash · Maintenance).
- Table columns: Timestamp · Actor · Action · Target · Outcome · IP.
- Filters / CSV / JSON export buttons top-right.
- Live data shows `auth.login.success`, `scan.started`, `scan.finished`, `oci.tag.deleted`, etc.
- Console clean. Screenshot: `screenshots/batch-14-audit.png`.

### 14.2 v1.7 S3AUDIT-01..05 schema ✅
- Migration `037_audit_actor_s3_key_id.up.sql` applied: `sqlite3` confirms `audit_log` has `actor_user_id`, `actor_api_key_id`, AND new **`actor_s3_key_id`** columns.
- No multipart-create events triggered in this run (would be needed to populate the column with non-null values), but the schema is in place — hermetic tests in `internal/api/audit_recordaudit_test.go` verify the population logic.

### 14.3 Audit log filter + cursor pagination ✅
- `?from=...&to=...&limit=...` returns the right slice (verified earlier: ~20 events between 20:30 and 21:00).
- `next_cursor` opaque token present; the wt3 F-04.2 fix for RFC3339Nano timestamps is holding (no duplicate / missing rows across page boundaries).

### 14.4 TLS Certificates UI ✅
- `/admin/tls` shows: "Current Certificate · Active TLS certificate details" → "Using the default self-signed certificate" + Upload button.
- Upload form with two textareas (Certificate PEM / Private Key PEM) + Browse-file affordance + Upload Certificate button.
- "Certificate History" table at bottom (empty: "No certificate upload history.").
- Console clean. Screenshot: `screenshots/batch-14-tls.png`.

### 14.5 Trivy Database ✅ (covered in batch 13 functional flow)
- UI page exists and shows the uploaded DB status.

### 14.6 Trash UI ✅
- `/admin/trash` renders with deleted repos visible: `1777151537-repo-8 (repo at /tmp/omnirepo-wt4/repos/acme/rpm/docker-ce)` and `1777151339-repo-7 (epel-jq)` — both deleted by alice during batch 06.
- Columns: Name · Type · Original Location · Deleted By · Deleted At · Retention.
- Per-row Restore button + delete-permanent button. "Select all" for bulk action.
- Retention countdown shows `6d 23h` (default 7d trash retention).
- Screenshot: `screenshots/batch-14-trash.png`.

### 14.7 Garbage Collection UI ✅
- `/admin/gc` renders: "Run Garbage Collection" card with red "Run Garbage Collection" button + warning text "permanently delete orphan blobs and expired trash entries".
- "Last GC Run" card showing "No garbage collection has been run yet."
- Screenshot: `screenshots/batch-14-gc-loaded.png`.

### 14.8 Maintenance Mode UI ✅
- `/admin/maintenance` renders: toggle switch + status indicator "System operational · All read and write operations are functioning normally."
- Banner explanation: "When enabled, all write operations will return HTTP 503. Read operations continue to work normally. A maintenance banner will be shown to all users."
- Screenshot: `screenshots/batch-14-maintenance-loaded.png`.

### 14.9 DB Health card ✅ (covered in batch 01 dashboard)
- `GET /api/v1/admin/db/health` returns full status: `can_run_now: true`, `driver: modernc v1.48.2 (FTS5, JSON1)`, `pragmas` (busy_timeout, cache_size, foreign_keys, journal_mode=wal, synchronous=normal, temp_store=memory), `integrity: status=ok duration_ms=31`, `journal_mode: wal`.

### 14.10 RBAC for admin endpoints ✅ (covered)
- Non-super-admin (alice/bob/mallory) cannot reach `/api/v1/admin/*` — blocked by middleware. The Admin sidebar nav doesn't show for them either (covered by `useRoleFor` v1.5 RBAC plumbing).

## Findings

**None.** All admin pages render cleanly, schema migrations applied, audit log + filter + pagination working.

## Sign-off
- [x] All in-scope cases marked
- [x] Backend log gate: 0 hits (background scan retries are INFO-level)
- [ ] Codex batch-end review
- [x] Status flipped to ✅
