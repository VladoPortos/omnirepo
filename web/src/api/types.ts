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
  created_at: string;
}

export interface RepoCreate {
  name: string;
  type: RepoType;
  description_md?: string;
  auto_scan?: boolean;
  block_on_severity?: BlockSeverity;
  public_read?: boolean;
}

export interface RepoPatch {
  description_md?: string;
  auto_scan?: boolean;
  block_on_severity?: BlockSeverity;
  public_read?: boolean;
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

export interface AuditEvent {
  id: number;
  timestamp: string;
  actor: string;
  action: string;
  target_kind: string;
  target_id: string;
  outcome: string;
  ip: string;
  user_agent: string;
  details: Record<string, unknown>;
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
  source: 'baked-in' | 'uploaded' | 'online-pulled';
  stale: boolean;
  applied_at: string;
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
}

export interface DashboardActivityItem {
  id: number;
  action: string;
  target_id: string;
  created_at: string;
}

export interface DashboardResponse {
  storage_used_bytes: number;
  storage_total_bytes: number;
  repo_count: number;
  user_count: number;
  scan_findings: DashboardScanFindings;
  recent_activity: DashboardActivityItem[];
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
