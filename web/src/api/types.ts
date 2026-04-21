/**
 * TypeScript types mirroring the OpenAPI 3.1 spec at internal/api/openapi.yaml.
 */

// -- Generic --

export interface PaginatedResponse<T> {
  items: T[];
  next_cursor: string | null;
}

export interface ErrorResponse {
  error: string;
  detail: string;
}

// -- Auth --

export interface LoginRequest {
  login: string;
  password: string;
}

// -- First-run setup --

export interface SetupStatusResponse {
  needs_setup: boolean;
}

export interface SetupSuperAdminRequest {
  login: string;
  email: string;
  password: string;
}

export interface SetupSuperAdminResponse {
  login: string;
  is_super_admin: boolean;
}

// -- Repo content listing --

export interface RepoContentEntry {
  id?: number;
  name: string;
  version?: string;
  size_bytes: number;
  uploaded_at: string;
  scan_severity?: string;
  latest_scan_id?: number;
  extra?: Record<string, unknown>;
}

// F-T18: paginated content response. Total is the row count without
// limit/offset applied. next_offset is null/undefined when the caller has
// reached the end — use it verbatim as the next request's offset.
export interface RepoContentPage {
  items: RepoContentEntry[];
  total: number;
  next_offset?: number | null;
}

export interface LoginResponse {
  login: string;
  is_super_admin: boolean;
  must_change_password: boolean;
}

export interface ChangePasswordRequest {
  current: string;
  new_password: string;
}

// -- User --

export interface User {
  id: number;
  login: string;
  email: string;
  is_super_admin: boolean;
  must_change_password: boolean;
  avatar_seed: string;
  created_at: string;
  // Populated only when /admin/users was queried with include_deleted=true
  // and this row is soft-deleted (F-7 admin half). Absent on live rows so
  // existing callers stay unaffected.
  deleted_at?: string;
}

export interface MeResponse {
  id: number;
  login: string;
  email: string;
  is_super_admin: boolean;
  must_change_password: boolean;
  avatar_seed: string;
}

export interface MeUpdateRequest {
  email?: string;
  avatar_seed?: string;
}

export interface UserCreate {
  login: string;
  email: string;
}

export interface UserCreateResponse {
  login: string;
  one_time_password: string;
}

export interface UserUpdate {
  email?: string;
  is_super_admin?: boolean;
}

// -- Project --

export interface Project {
  id: number;
  name: string;
  description_md: string;
  created_at: string;
}

export interface ProjectListItem {
  id: number;
  name: string;
  description_md: string;
  member_count: number;
  repo_count: number;
  size_bytes: number;
  created_at: string;
}

export interface ProjectMember {
  user_id: number;
  login: string;
  email: string;
}

export interface ProjectRepo {
  id: number;
  type: RepoType;
  name: string;
  description_md: string;
  size_bytes: number;
  auto_scan: boolean;
  public_read: boolean;
  created_at: string;
}

export interface ProjectDetail {
  id: number;
  name: string;
  description_md: string;
  created_at: string;
  members: ProjectMember[];
  repos: ProjectRepo[];
  buckets: ProjectBucket[];
}

// Bucket as projected into the project detail response. `size_bytes` is
// live-computed (SUM s3_objects.size_bytes); `object_count` mirrors it.
export interface ProjectBucket {
  id: number;
  name: string;
  size_bytes: number;
  object_count: number;
  created_at: string;
}

// BucketDetail matches the GET /s3-buckets/{bucket} response.
export interface BucketDetail {
  name: string;
  size_bytes: number;
  object_count: number;
  created_at: string;
}

export interface BucketObjectItem {
  key: string;
  size_bytes: number;
  etag: string;
  content_type?: string;
  sha256?: string;
  last_modified: string;
}

export interface BucketObjectsPage {
  items: BucketObjectItem[];
  next_marker?: string;
  truncated: boolean;
}

export interface BucketCreate {
  name: string;
}

export interface ProjectCreate {
  name: string;
  description_md?: string;
}

export interface ProjectCreateResponse {
  id: number;
  name: string;
}

export interface ActivityItem {
  id: number;
  action: string;
  actor_user_id?: number;
  target_kind: string;
  target_id: string;
  outcome?: string;
  details?: string;
  created_at: string;
}

// -- Repo --

export type RepoType = 'docker' | 'rpm' | 'deb' | 'pypi' | 'helm' | 'git' | 'raw' | 's3';

export type BlockSeverity = 'none' | 'low' | 'medium' | 'high' | 'critical';

export interface Repo {
  id: number;
  project_id: number;
  type: RepoType;
  name: string;
  description_md: string;
  auto_scan: boolean;
  block_on_severity: BlockSeverity;
  public_read: boolean;
  size_bytes: number;
  // F-T15: per-type artifact count. Meaning depends on type:
  //   docker=tagged images, rpm/pypi/helm/raw=stored files, deb=distinct
  //   (package,arch), git=ref count. 0 is a valid empty repo.
  item_count: number;
  created_at: string;

  // Phase 8 Plan 04 (MIRROR-16..21) — mirror fields emitted by
  // internal/api/repos.go:repoResponse + repoListItem. Only meaningful
  // when `type ∈ {deb,rpm,pypi,helm}` and `is_mirror === true`. For
  // non-mirror repos the server emits `is_mirror: false`, empty strings
  // for the two text fields, and `mirror_cred_id: null`.
  //
  // `mirror_filter_json` is the raw JSON blob stored in the database
  // (TEXT column). The UI parses it into AptFilter / RpmFilter /
  // PypiFilter / HelmFilter — PascalCase keys matching the Go
  // SyncFilter struct fields in
  // internal/protocol/{deb,rpm,pypi,helm}/upstream_parse.go. Those
  // structs carry NO `json:` tags, so encoding/json serialises field
  // names verbatim (Names, Globs, Suites, Components, Arches).
  is_mirror: boolean;
  mirror_upstream_url: string;
  mirror_filter_json: string;
  mirror_cred_id: number | null;
  scan_on_sync: boolean;
}

// -- Mirror filters (Phase 8 Plan 04) --------------------------------------
//
// Wire format is PascalCase because the Go SyncFilter structs carry no
// `json:` tags — encoding/json falls back to field names verbatim.
// Confirmed in internal/protocol/{deb,rpm,pypi,helm}/upstream_parse.go.
// Do NOT rename these keys to snake_case; the backend validator
// (internal/api/mirror_validate.go:validateMirrorFilter) matches them
// exactly.

export interface AptFilter {
  Suites?: string[];
  Components?: string[];
  Arches?: string[];
  Names?: string[];
  Globs?: string[];
}

export interface RpmFilter {
  Names?: string[];
  Globs?: string[];
}

export interface PypiFilter {
  Names?: string[];
  Globs?: string[];
}

export interface HelmFilter {
  Names?: string[];
  Globs?: string[];
}

export type AnyFilter = AptFilter | RpmFilter | PypiFilter | HelmFilter;

// MirrorConfigValue is the shape the CreateRepoDialog + RepoSettingsTab
// pass in and out of MirrorConfigSection. Aligned with the fields the
// backend POST /repos and PATCH /repos/{type}/{repo} endpoints accept
// (see internal/api/types_phase1.go:CreateRepoRequest +
// internal/api/repos.go:repoPatchRequest).
export interface MirrorConfigValue {
  is_mirror: boolean;
  mirror_upstream_url: string;
  mirror_filter: AnyFilter;
  mirror_cred_id: number | null;
  scan_on_sync: boolean;
}

export interface RepoCreate {
  name: string;
  type: RepoType;
  description_md?: string;
  auto_scan?: boolean;
  block_on_severity?: BlockSeverity;
  public_read?: boolean;

  // Phase 8 Plan 04 (MIRROR-16..21) mirror creation fields. The backend
  // validates in five branches (internal/api/repos.go + mirror_validate.go):
  //   - type ∈ {deb,rpm,pypi,helm} when is_mirror=true
  //   - mirror_upstream_url is http(s) with non-empty host
  //   - mirror_filter parses as the protocol's SyncFilter (PascalCase)
  //   - mirror_cred_id belongs to the same project as the repo
  //   - scan_on_sync is a plain bool
  is_mirror?: boolean;
  mirror_upstream_url?: string;
  mirror_filter?: AnyFilter;
  mirror_cred_id?: number | null;
  scan_on_sync?: boolean;
}

export interface RepoPatch {
  description_md?: string;
  auto_scan?: boolean;
  block_on_severity?: BlockSeverity;
  public_read?: boolean;

  // Phase 8 Plan 04 — mirror-repo editable fields. is_mirror and
  // mirror_upstream_url are NOT in this shape: the backend rejects them
  // with 400 repo.mirror_url_immutable per D-02. Only filter, cred, and
  // scan_on_sync may change post-creation.
  mirror_filter?: AnyFilter;
  mirror_cred_id?: number | null;
  scan_on_sync?: boolean;
}

// SyncEnqueueResponse — the 202 body emitted by POST /sync for mirror
// repos (empty body POST). Same shape as PullExternalResponse for Docker
// clone, but scoped differently on the wire so the TS names stay clear.
export interface SyncEnqueueResponse {
  job_id: number;
  kind: string;
}

export interface WipeResponse {
  artifact_count: number;
  bytes_freed: number;
  trash_id: string;
}

// -- Sync --

export interface SyncRequest {
  source_url: string;
  cred_id?: number;
}

export type SyncJobStatus = 'pending' | 'running' | 'done' | 'failed';

export interface SyncJob {
  id: number;
  kind: string;
  repo_id: number;
  status: SyncJobStatus;
  log: string;
  error: string;
  created_at: string;
  started_at: string;
  finished_at: string;
}

// JobStatus alias for the subset used by live progress polling
// (Phase 8 / plan 08-03). Same wire tokens as SyncJobStatus.
export type JobStatus = SyncJobStatus;

/**
 * JobDetail mirrors the response from GET
 * /api/v1/projects/{name}/repos/{type}/{repo}/sync-jobs/{id} per Phase 8
 * plan 08-02 (`internal/api/repos_list.go:syncJobItem`). progress_bytes,
 * total_bytes, and current_step are always present in the payload — the
 * backend COALESCE-defaults them to 0 / 0 / "" so the UI can render a
 * deterministic cold-start frame. `last_error` is a plain string (NOT
 * an ApiErrorEnvelope) because the backend writes sync_jobs.last_error
 * as a pre-envelope operator-facing string; the UI wraps it into a
 * local envelope for rendering via ErrorEnvelopeRenderer.
 */
export interface JobDetail {
  id: number;
  kind: string;
  status: JobStatus;
  attempts: number;
  last_error?: string;
  payload_json?: string;
  log?: string;
  progress_bytes: number;
  total_bytes: number;
  current_step: string;
  /**
   * Quick task 260420-d03: count of files newly added during the sync.
   * Written once at sync completion by each protocol handler (not via the
   * throttled progress path) so it's 0 for running jobs. The success pill
   * uses this to render "Sync complete · N files · X MB" (D-03 literal
   * shape). Backend COALESCEs missing rows to 0.
   */
  files_synced: number;
  created_at: string;
  updated_at: string;
}

/**
 * PullExternalRequest matches the Go wire shape at
 * `internal/protocol/oci/pull_external.go:PullExternalRequest`. Key
 * naming delta from plan 08-03's original sketch: the backend uses
 * `src_image` + `dst_tag` (NOT `src` + `retag_as`). The plan's
 * `scan_override` field is NOT accepted by the v1.1 backend endpoint —
 * the repo's stored `auto_scan` flag governs per-pull scanning. UI
 * consumers may still render a scan-override checkbox, but we only
 * send the four fields the backend accepts.
 */
export interface PullExternalRequest {
  src_image: string;
  dst_tag?: string;
  cred_id?: number;
  src_username?: string;
  src_password?: string;
}

/**
 * PullExternalResponse is the enqueue response emitted by the
 * /pull-external handler — a plain `{ job_id }` at HTTP 202.
 */
export interface PullExternalResponse {
  job_id: number;
}

/**
 * UpstreamCred mirrors the secret-free upstreamCredResponse struct in
 * `internal/api/upstream_creds.go`. Consumed by the CloneImageDialog
 * credential picker AND by the Phase 8 Plan 05 Upstream credentials
 * tab on ProjectSettingsPage.
 *
 * CRITICAL SECURITY PROPERTY (T-08-05-01): password and token fields
 * are absent from this type on purpose. The backend's
 * `upstreamCredResponse` (internal/api/upstream_creds.go) never echoes
 * secrets; adding them to this type would violate the shape contract
 * AND make it possible for the UI to render a secret by accident. Do
 * NOT add them.
 */
export interface UpstreamCred {
  id: number;
  host: string;
  kind: string;
  username: string;
  created_at: string;
  updated_at: string;
}

/**
 * UpstreamCredKind — the five credential "kinds" the backend accepts.
 * Mirrors `metadata.ValidCredKinds` in internal/metadata/upstream_creds.go.
 * The UI surfaces a single canonical 'apt' token; the obsolete 'deb'
 * alias was removed in Phase 9 (POLISH-02). Clients that still submit
 * `kind="deb"` get a 400 envelope with code `validation.invalid_cred_kind`
 * and a machine-readable migration hint.
 */
export type UpstreamCredKind = 'docker' | 'apt' | 'rpm' | 'pypi' | 'helm';

/**
 * UpstreamCredCreate — POST /api/v1/projects/{name}/upstream-creds
 * body. Matches `upstreamCredCreateRequest` in
 * internal/api/upstream_creds.go. password and token are write-only;
 * the backend rejects with `password_or_token_required` if BOTH are
 * blank. The UI normalises empty strings to `undefined` before POST so
 * the server sees the field as absent.
 */
export interface UpstreamCredCreate {
  host: string;
  kind: UpstreamCredKind;
  username?: string;
  password?: string;
  token?: string;
}

/**
 * UpstreamCredPatch — PATCH body shape. Mirrors
 * `upstreamCredCreateRequest` reused by handleUpdateUpstreamCred.
 * CONTRACT (T-08-05-03): password and token fields, when OMITTED from
 * the JSON body, instruct the backend to KEEP the existing secret. An
 * empty string would also be treated as "no change" by the backend's
 * metadata.UpstreamCreds.Update — but the UI MUST never send an empty
 * string for these fields, because any future backend change might
 * reinterpret `""` as "wipe". The safe client contract is: omit the
 * key entirely when the operator leaves the field blank in edit mode.
 */
export interface UpstreamCredPatch {
  host?: string;
  kind?: UpstreamCredKind;
  username?: string;
  password?: string;
  token?: string;
}

// -- Scan --

export type ScanStatus = 'pending' | 'running' | 'done' | 'failed';

export interface Scan {
  id: number;
  repo_id: number;
  artifact_kind: string;
  artifact_id: string;
  status: ScanStatus;
  attempts: number;
  last_error: string;
  severity_summary_json: string;
  sbom_path: string;
  trivy_db_version: string;
  created_at: string;
  started_at: string;
  finished_at: string;
}

export type Severity = 'CRITICAL' | 'HIGH' | 'MEDIUM' | 'LOW' | 'UNKNOWN';

export interface Vulnerability {
  id: number;
  scan_id: number;
  cve_id: string;
  severity: Severity;
  package_name: string;
  package_version: string;
  fixed_version: string;
  title: string;
  description: string;
}

export interface SBOMRef {
  scan_id: number;
  format: string;
  download_url: string;
}

// -- Search --

export type SearchKind = 'repo' | 'artifact' | 'cve' | 'rpm' | 'deb' | 'pypi' | 'helm';

export interface SearchResult {
  kind: SearchKind;
  entity_id: number;
  name: string;
  location: string;
  severity: string;
  score: number;
}

export interface SearchResponse {
  items: SearchResult[];
  next_cursor: string | null;
}

// -- Audit --

// ME-10: Backend returns nullable fields (actor/ip/outcome/etc.) with
// omitempty and the `ok` outcome token for successful events. Match the
// shape so anonymous/system events don't surface as undefined.
export interface AuditEvent {
  id: number;
  timestamp: string;
  actor?: string | null;
  action: string;
  target_kind?: string | null;
  target_id?: string | null;
  outcome?: string | null;
  ip?: string | null;
  user_agent?: string | null;
  details?: string | null;
}

export interface AuditListResponse {
  items: AuditEvent[];
  next_cursor: string | null;
}

// -- TLS --

export interface TLSCertInfo {
  subject: string;
  issuer: string;
  not_before: string;
  not_after: string;
  serial: string;
  fingerprint_sha256: string;
  source: 'self-signed' | 'uploaded';
}

export interface TLSHistoryEntry {
  uploaded_at: string;
  uploaded_by: string;
  subject: string;
  fingerprint_sha256: string;
}

// -- Trivy DB --

export interface TrivyDBStatus {
  version: string;
  age_hours: number;
  source: 'baked-in' | 'uploaded' | 'online-pulled' | 'none';
  stale: boolean;
  applied_at?: string;
  size_bytes?: number;
  path?: string;
}

export interface TrivyDBPullStatus {
  state: 'idle' | 'running' | 'success' | 'failure';
  bytes_downloaded: number;
  started_at?: string;
  finished_at?: string;
  error?: string;
}

// -- GC --

export interface GCTriggerResponse {
  job_id: number;
}

export type GCStatus = 'idle' | 'running' | 'done' | 'failed';

export interface GCStatusResponse {
  status: GCStatus;
  job_id: number;
  started_at: string;
  finished_at: string;
  bytes_freed: number;
}

// -- Maintenance --

export interface MaintenanceStatus {
  enabled: boolean;
}

export interface MaintenanceToggle {
  enabled: boolean;
}

// -- Trash --

export interface TrashEntry {
  id: string;
  name: string;
  type: string;
  original_location: string;
  deleted_by: string;
  deleted_at: string;
  retention_countdown: string;
}

export interface TrashListResponse {
  items: TrashEntry[];
  next_cursor: string | null;
}

// -- Settings --

export type SettingsMap = Record<string, string>;

export type SettingsPatch = Record<string, string>;

// -- Dashboard --

export interface DashboardScanFindings {
  critical: number;
  high: number;
  medium?: number;
  low?: number;
}

export interface DashboardActivityItem {
  id: number;
  action: string;
  target_id: string;
  created_at: string;
}

export interface DashboardVulnRow {
  cve_id: string;
  severity: string;
  package: string;
  project: string;
  repo: string;
  repo_type: string;
}

export interface DashboardResponse {
  storage_used_bytes: number;
  storage_total_bytes: number;
  project_count: number;
  repo_count: number;
  user_count: number;
  scan_findings: DashboardScanFindings;
  high_severity: DashboardVulnRow[];
  recent_activity: DashboardActivityItem[];
}

export interface StorageRepoRow {
  project: string;
  name: string;
  type: string;
  size_bytes: number;
}

export interface DashboardStorageResponse {
  total_bytes: number;
  used_bytes: number;
  repos: StorageRepoRow[];
}

// -- Git --

export type GitEntryType = 'blob' | 'tree' | 'commit';

export interface GitTreeEntry {
  name: string;
  path: string;
  type: GitEntryType;
  size: number;
  sha: string;
}

export interface GitFileContent {
  name: string;
  path: string;
  sha: string;
  size: number;
  encoding: string;
  content: string;
}

export interface GitCommit {
  sha: string;
  message: string;
  author_name: string;
  author_email: string;
  author_date: string;
  committer_name: string;
  committer_email: string;
  committer_date: string;
  parent_shas: string[];
}

export interface GitDiffFile {
  path: string;
  status: string;
  patch: string;
}

export interface GitDiff {
  sha: string;
  message: string;
  stats: {
    additions: number;
    deletions: number;
    files_changed: number;
  };
  files: GitDiffFile[];
}

export interface GitBlame {
  path: string;
  lines: Array<{
    line_number: number;
    sha: string;
    author: string;
    date: string;
    content: string;
  }>;
}

export type GitRefType = 'branch' | 'tag';

export interface GitRef {
  name: string;
  type: GitRefType;
  sha: string;
}

export interface GitCompareResponse {
  base: string;
  head: string;
  ahead_by: number;
  behind_by: number;
  commits: GitCommit[];
  files: GitDiffFile[];
}

// -- API Keys --

export interface APIKey {
  id: number;
  prefix: string;
  name: string;
  created_at: string;
  last_used_at: string | null;
}

export interface APIKeyCreate {
  name: string;
}

export interface APIKeyCreateResponse {
  id: number;
  prefix: string;
  secret: string;
  name: string;
  created_at: string;
}

// -- S3 Keys --

export interface S3Key {
  id: number;
  access_key_id: string;
  project_id: number;
  created_at: string;
  last_used_at: string;
}

export interface S3KeyCreate {
  project_id: number;
}

export interface S3KeyCreateResponse {
  id: number;
  access_key_id: string;
  secret_access_key: string;
  project_id: number;
  created_at: string;
}
