/**
 * TanStack Query hooks for OmniRepo REST API.
 * Query keys structured as ['entity', ...params] per D-36.
 */

import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { useMemo, useState, useCallback } from 'react';
import { api, ApiError } from './client';

// enc encodes a path segment from a route-param (e.g. projectName, bucketName,
// repoName) before interpolating into the URL. Defense-in-depth: the router
// already constrains these slugs with regex, and the backend re-validates,
// but any caller that forgets the guard (e.g. a new feature, a route-state
// migration) could otherwise ship raw user input straight into the URL.
// encodeURIComponent is a no-op for our valid-slug set [a-z0-9._-] so this
// is free on the golden path.
const enc = encodeURIComponent;
import type {
  MeResponse,
  MeUpdateRequest,
  LoginRequest,
  LoginResponse,
  SetupStatusResponse,
  SetupSuperAdminRequest,
  SetupSuperAdminResponse,
  RepoContentEntry,
  RepoContentPage,
  ProjectListItem,
  ProjectDetail,
  ProjectCreate,
  ProjectCreateResponse,
  ActivityItem,
  PullExternalRequest,
  PullExternalResponse,
  UpstreamCred,
  UpstreamCredCreate,
  UpstreamCredPatch,
  Repo,
  RepoCreate,
  RepoPatch,
  DashboardResponse,
  DashboardStorageResponse,
  SearchResponse,
  PaginatedResponse,
  SyncEnqueueResponse,
  GitTreeEntry,
  GitFileContent,
  GitCommit,
  GitDiff,
  GitBlame,
  GitRef,
  GitCompareResponse,
  APIKey,
  APIKeyCreate,
  APIKeyCreateResponse,
  S3Key,
  S3KeyCreate,
  S3KeyCreateResponse,
  ProjectBucket,
  BucketDetail,
  BucketObjectsPage,
  BucketCreate,
  Scan,
  ScanStatus,
  Vulnerability,
} from './types';

// -- Repo-level scans list (populates the Scan Results tab on every repo page) --

export function useRepoScans(
  projectName: string,
  repoType: string,
  repoName: string,
  opts?: { status?: ScanStatus; limit?: number; offset?: number },
) {
  return useQuery({
    queryKey: [
      'repo-scans',
      projectName,
      repoType,
      repoName,
      opts?.status ?? '',
      opts?.limit ?? 100,
      opts?.offset ?? 0,
    ],
    queryFn: () => {
      const params: Record<string, string> = {};
      if (opts?.status) params.status = opts.status;
      if (opts?.limit != null) params.limit = String(opts.limit);
      if (opts?.offset != null) params.offset = String(opts.offset);
      return api.get<Scan[]>(
        `/projects/${enc(projectName)}/repos/${enc(repoType)}/${enc(repoName)}/scans`,
        params,
      );
    },
    // Scans progress; keep data fresh while a user watches the tab.
    staleTime: 5_000,
    refetchInterval: (query) => {
      const data = query.state.data as Scan[] | undefined;
      if (!data) return false;
      return data.some((s) => s.status === 'pending' || s.status === 'running') ? 3_000 : false;
    },
  });
}

/**
 * usePruneScans — POST /projects/.../scans/prune which deletes every
 * finished scan row except the newest per (artifact_kind, artifact_id).
 * Running/pending rows are preserved. Returns {deleted, kept}.
 */
export function usePruneScans(
  projectName: string,
  repoType: string,
  repoName: string,
) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () =>
      api.post<{ deleted: number; kept: number }>(
        `/projects/${enc(projectName)}/repos/${enc(repoType)}/${enc(repoName)}/scans/prune`,
        {},
      ),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['repo-scans', projectName, repoType, repoName] });
    },
  });
}

/**
 * useRescanRepo — POST /projects/{name}/repos/{type}/{repo}/rescan,
 * which enqueues a fresh Trivy scan for every artifact currently in the
 * repo. Invalidates the repo-scans query on success so the progress
 * appears immediately in the Scan Results tab.
 */
export function useRescanRepo(
  projectName: string,
  repoType: string,
  repoName: string,
) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () =>
      api.post<{ enqueued: number; repo_type: string; pool_kicked: boolean }>(
        `/projects/${enc(projectName)}/repos/${enc(repoType)}/${enc(repoName)}/rescan`,
        {},
      ),
    onSuccess: () => {
      qc.invalidateQueries({
        queryKey: ['repo-scans', projectName, repoType, repoName],
      });
    },
  });
}

/**
 * useScan — GET /scans/{id} for a single scan row. Used by the
 * per-artifact scan report page.
 */
export function useScan(scanID: number | null | undefined) {
  return useQuery({
    queryKey: ['scan', scanID ?? 0],
    enabled: scanID != null && scanID > 0,
    queryFn: () => api.get<Scan>(`/scans/${scanID}`),
  });
}

/**
 * useScanVulnerabilities — GET /scans/{id}/vulnerabilities. Backend
 * caps the response at 1000 rows; that's the v1 contract.
 */
export function useScanVulnerabilities(scanID: number | null | undefined) {
  return useQuery({
    queryKey: ['scan-vulns', scanID ?? 0],
    enabled: scanID != null && scanID > 0,
    queryFn: () => api.get<Vulnerability[]>(`/scans/${scanID}/vulnerabilities`),
  });
}

/**
 * sbomDownloadURL — the API path the browser hits to download the
 * CycloneDX SBOM for a scan. Exposed as a helper so the report page
 * can render a plain `<a href>` (no fetch → no CORS surprises).
 */
export function sbomDownloadURL(scanID: number): string {
  return `/api/v1/scans/${scanID}/sbom`;
}

/**
 * useRescanArtifact — POST /artifacts/{id}/rescan for a single row in
 * the content table. Invalidates both repo-scans and repo-content so
 * the UI flips the row's badge from Clean → Scanning → Clean without
 * a manual refresh.
 */
export function useRescanArtifact(
  projectName: string,
  repoType: string,
  repoName: string,
) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (artifactID: string) =>
      api.post<{ scan_id: number }>(
        `/projects/${enc(projectName)}/repos/${enc(repoType)}/${enc(repoName)}/artifacts/${enc(artifactID)}/rescan`,
        {},
      ),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['repo-scans', projectName, repoType, repoName] });
      qc.invalidateQueries({ queryKey: ['repo-content', projectName, repoType, repoName] });
    },
  });
}

// -- Repo content (listing artifacts uploaded to a repo) --

// useRepoContent returns the entries array for the page. Existing consumers
// treat the data as a plain array — preserved via `select`. For pagination
// metadata (total, next_offset) use useRepoContentPage below.
export function useRepoContent(
  projectName: string,
  repoType: string,
  repoName: string,
  opts?: { limit?: number; offset?: number },
) {
  return useQuery({
    queryKey: ['repo-content', projectName, repoType, repoName, opts?.limit ?? 100, opts?.offset ?? 0],
    queryFn: () => fetchRepoContentPage(projectName, repoType, repoName, opts),
    staleTime: 15_000,
    select: (page: RepoContentPage) => page.items,
    // While any row is mid-scan (status="scanning"), poll so the severity
    // badge lights up as scans finish — otherwise the user sees "Not
    // scanned" frozen until they refresh.
    refetchInterval: (query) => {
      const page = query.state.data as RepoContentPage | undefined;
      if (!page) return false;
      return page.items.some((r) => r.scan_severity === 'scanning') ? 3_000 : false;
    },
  });
}

// useRepoContentPage returns the full paginated envelope so load-more
// tables can read total / next_offset. F-T18.
export function useRepoContentPage(
  projectName: string,
  repoType: string,
  repoName: string,
  opts?: { limit?: number; offset?: number },
) {
  return useQuery({
    queryKey: ['repo-content', projectName, repoType, repoName, opts?.limit ?? 100, opts?.offset ?? 0],
    queryFn: () => fetchRepoContentPage(projectName, repoType, repoName, opts),
    staleTime: 15_000,
    refetchInterval: (query) => {
      const page = query.state.data as RepoContentPage | undefined;
      if (!page) return false;
      return page.items.some((r) => r.scan_severity === 'scanning') ? 3_000 : false;
    },
  });
}

function fetchRepoContentPage(
  projectName: string,
  repoType: string,
  repoName: string,
  opts?: { limit?: number; offset?: number },
): Promise<RepoContentPage> {
  const params: Record<string, string> = {};
  if (opts?.limit != null) params.limit = String(opts.limit);
  if (opts?.offset != null) params.offset = String(opts.offset);
  return api.get<RepoContentPage>(
    `/projects/${enc(projectName)}/repos/${enc(repoType)}/${enc(repoName)}/content`,
    params,
  );
}

// useRepoContentLoadMore wraps useRepoContentPage with append-forward
// offset state — fits the existing /loop-more pattern used by the
// per-row-scan feature (commit 8ffe66c). Call .loadMore() to fetch and
// concatenate the next window; .hasMore tracks whether the backend is
// still returning a next_offset. F-T18.
export function useRepoContentLoadMore(
  projectName: string,
  repoType: string,
  repoName: string,
  pageSize = 100,
) {
  const [offset, setOffset] = useState(0);
  const [accumulated, setAccumulated] = useState<RepoContentEntry[]>([]);
  const [total, setTotal] = useState(0);
  const [hasMore, setHasMore] = useState(true);

  // Each page is cached under the shared ['repo-content', ...] key so the
  // scan-polling refetch from useRepoContent also refreshes partials.
  const q = useQuery({
    queryKey: ['repo-content', projectName, repoType, repoName, pageSize, offset],
    queryFn: async () => {
      const page = await fetchRepoContentPage(projectName, repoType, repoName, {
        limit: pageSize,
        offset,
      });
      setAccumulated((prev) =>
        offset === 0 ? page.items : [...prev, ...page.items],
      );
      setTotal(page.total);
      setHasMore(page.next_offset != null);
      return page;
    },
    staleTime: 15_000,
  });

  const loadMore = useCallback(() => {
    if (!q.data?.next_offset || q.data.next_offset === offset) return;
    setOffset(q.data.next_offset);
  }, [q.data, offset]);

  const reset = useCallback(() => {
    setOffset(0);
    setAccumulated([]);
    setHasMore(true);
  }, []);

  // Reset accumulator when the target repo changes. Avoids leaking rows
  // from repo A into repo B when a user navigates between them.
  useMemo(() => {
    reset();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [projectName, repoType, repoName]);

  return {
    items: accumulated,
    total,
    hasMore,
    loadMore,
    isLoading: q.isLoading,
    isFetching: q.isFetching,
    error: q.error,
  };
}

// -- First-run setup --

export function useSetupStatus() {
  return useQuery({
    queryKey: ['setup', 'status'],
    queryFn: () => api.get<SetupStatusResponse>('/setup/status'),
    // Once we know it's false we don't want to keep re-polling; once it's
    // true the only state change is "user creates super-admin" which the
    // mutation's onSuccess handles directly.
    staleTime: Infinity,
    retry: false,
  });
}

export function useSetupSuperAdmin() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (data: SetupSuperAdminRequest) =>
      api.post<SetupSuperAdminResponse>('/setup/superadmin', data),
    onSuccess: () => {
      qc.setQueryData<SetupStatusResponse>(['setup', 'status'], { needs_setup: false });
    },
  });
}

// -- Auth / Me --

export function useMe() {
  return useQuery({
    queryKey: ['me'],
    queryFn: async () => {
      try {
        return await api.get<MeResponse>('/me');
      } catch (err) {
        if (err instanceof ApiError && (err.status === 401 || err.status === 403)) {
          return null;
        }
        throw err;
      }
    },
    staleTime: 60_000,
    retry: false,
  });
}

export function useLogin() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (creds: LoginRequest) =>
      api.post<LoginResponse>('/auth/login', creds),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['me'] }),
  });
}

export function useLogout() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => api.post<void>('/auth/logout'),
    onSuccess: () => {
      qc.clear();
      window.location.href = '/login';
    },
  });
}

export function useChangePassword() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (data: { current: string; new_password: string }) =>
      api.post<void>('/auth/change-password', { current: data.current, new: data.new_password }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['me'] }),
  });
}

// -- Dashboard --

export function useDashboard() {
  return useQuery({
    queryKey: ['dashboard'],
    queryFn: () => api.get<DashboardResponse>('/dashboard'),
    staleTime: 15_000,
  });
}

export function useDashboardStorage() {
  return useQuery({
    queryKey: ['dashboard', 'storage'],
    queryFn: () => api.get<DashboardStorageResponse>('/dashboard/storage'),
    staleTime: 15_000,
  });
}

// -- Admin jobs summary (Phase 7 / D-06) --

/**
 * AdminJobsSummary mirrors the GET /api/v1/admin/jobs/summary response
 * shape (LOCKED at D-06). Timestamps are RFC3339 strings or null when no
 * matching sync_jobs row exists.
 */
export interface AdminJobsSummary {
  running: number;
  queued: number;
  failed_last_24h: number;
  last_completed_at: string | null;
  last_failed_at: string | null;
}

/**
 * useAdminJobsSummary — TanStack hook for the C-4 Background Jobs card.
 *
 * Pass `enabled = !!currentUser?.is_super_admin` so non-admins never
 * issue a 403-generating request (the server gate is RequireCan on
 * ActionTriggerGC). staleTime 60s per UI-SPEC §Interaction Patterns /
 * Threshold display — counts don't need to update every request.
 */
export function useAdminJobsSummary(enabled: boolean) {
  return useQuery({
    queryKey: ['admin', 'jobs', 'summary'],
    queryFn: () => api.get<AdminJobsSummary>('/admin/jobs/summary'),
    staleTime: 60_000,
    enabled,
  });
}

// -- Admin TLS current (Phase 7 / C-5) -------------------------------------

/**
 * AdminTLSCurrent mirrors the GET /api/v1/admin/tls/current response shape
 * emitted by internal/api/admin_tls_history.go:handleTLSCurrent. The
 * handler returns RFC3339 strings for not_before / not_after; `source` is
 * derived from on-disk state and is either "self-signed" (bootstrap cert
 * in use) or "uploaded" (operator-uploaded cert present on disk).
 */
export interface AdminTLSCurrent {
  subject: string;
  issuer: string;
  not_before: string;
  not_after: string;
  dns_names: string[];
  serial: string;
  fingerprint_sha256: string;
  source: 'self-signed' | 'uploaded';
}

/**
 * useAdminTLSCurrent — TanStack hook for the C-5 TLS Cert Expiry card.
 *
 * Pass `enabled = !!currentUser?.is_super_admin` so non-admins never
 * issue a request (server gate = RequireCan(ActionUploadTLSCert)).
 * staleTime 60s matches the other admin card hooks — expiry days don't
 * tick fast enough to warrant sub-minute polling.
 */
export function useAdminTLSCurrent(enabled: boolean) {
  return useQuery({
    queryKey: ['admin', 'tls', 'current'],
    queryFn: () => api.get<AdminTLSCurrent>('/admin/tls/current'),
    staleTime: 60_000,
    enabled,
  });
}

// -- Admin Trivy DB status (Phase 7 / C-6) ---------------------------------

/**
 * AdminTrivyDBStatus mirrors the GET /api/v1/admin/trivy/db/status
 * response shape emitted by internal/api/admin_trivy.go:handleTrivyDBStatus.
 *
 * Shape notes:
 *   - `age_hours` is -1 when no Trivy DB has been initialised (no meta
 *     row and no baked-in files on disk), or when a baked-in DB is
 *     detected but has no version metadata. The C-6 card maps
 *     `source === 'none'` (or `version === ''`) to the disabled /
 *     "Not initialised" state.
 *   - `source` is one of 'uploaded' (admin-uploaded tarball),
 *     'online-pulled' (internet fetch), 'baked-in' (shipped with the
 *     Docker image, no meta row), or 'none' (no DB at all).
 *   - `size_bytes` and `applied_at` are only populated when a meta row
 *     exists (source ∈ {'uploaded', 'online-pulled'}).
 *   - `stale` is the server's threshold verdict (age > scan.db_warn_age_days
 *     setting, default 7 days). The client uses `age_hours` directly
 *     via `trivyDBVariant` so the UI remains threshold-sovereign.
 */
export interface AdminTrivyDBStatus {
  version: string;
  source: 'uploaded' | 'online-pulled' | 'baked-in' | 'none';
  age_hours: number;
  stale: boolean;
  size_bytes?: number;
  applied_at?: string;
}

/**
 * useAdminTrivyDBStatus — TanStack hook for the C-6 Trivy DB Freshness
 * card. Pass `enabled = !!currentUser?.is_super_admin` so non-admins
 * never issue a request (server gate = RequireCan(ActionTriggerGC)).
 */
export function useAdminTrivyDBStatus(enabled: boolean) {
  return useQuery({
    queryKey: ['admin', 'trivy', 'db', 'status'],
    queryFn: () => api.get<AdminTrivyDBStatus>('/admin/trivy/db/status'),
    staleTime: 60_000,
    enabled,
  });
}

// -- Projects --

export function useProjects() {
  return useQuery({
    queryKey: ['projects'],
    queryFn: () => api.get<PaginatedResponse<ProjectListItem>>('/projects'),
    staleTime: 30_000,
  });
}

export function useProject(name: string) {
  return useQuery({
    queryKey: ['projects', name],
    queryFn: () => api.get<ProjectDetail>(`/projects/${enc(name)}`),
    enabled: !!name,
  });
}

export function useProjectActivity(name: string) {
  return useQuery({
    queryKey: ['projects', name, 'activity'],
    queryFn: () =>
      api.get<{ items: ActivityItem[] }>(`/projects/${enc(name)}/activity`),
    enabled: !!name,
    staleTime: 15_000,
  });
}

export function useCreateProject() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (data: ProjectCreate) =>
      api.post<ProjectCreateResponse>('/projects', data),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['projects'] });
      qc.invalidateQueries({ queryKey: ['dashboard'] });
    },
  });
}

export function useDeleteProject() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (name: string) => api.del<void>(`/projects/${enc(name)}`),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['projects'] });
      qc.invalidateQueries({ queryKey: ['dashboard'] });
    },
  });
}

// -- Project Members --
//
// Walkthrough 2026-04-20: the ProjectDetailPage picker needs the full user
// list so the operator can pick a non-member to add. The admin-users hook
// in UsersPage.tsx is local, so we expose a thin shared query here.

export interface ProjectMemberUser {
  id: number;
  login: string;
  email: string;
  is_super_admin: boolean;
}

export function useAdminUserList() {
  return useQuery({
    queryKey: ['admin', 'users', 'list'],
    // Codex review 2026-04-21: chase next_cursor so the project-member
    // picker isn't capped at the API's default page size. The server
    // enforces MaxPageLimit=200 per request; we request the max and loop
    // so deployments with >200 users still see every account.
    queryFn: async () => {
      const items: ProjectMemberUser[] = [];
      let cursor: string | undefined;
      // Hard cap loop iterations as a runaway-paging safety net.
      for (let i = 0; i < 50; i++) {
        const params: Record<string, string> = { limit: '200' };
        if (cursor) params.cursor = cursor;
        const page = await api.get<PaginatedResponse<ProjectMemberUser>>(
          '/admin/users',
          params,
        );
        items.push(...(page.items ?? []));
        if (!page.next_cursor) break;
        cursor = page.next_cursor;
      }
      return { items, next_cursor: null };
    },
    staleTime: 30_000,
  });
}

export function useAddProjectMember(projectName: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (login: string) =>
      api.post<void>(
        `/projects/${enc(projectName)}/members/${enc(login)}`,
        undefined,
      ),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['projects', projectName] });
    },
  });
}

export function useRemoveProjectMember(projectName: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (login: string) =>
      api.del<void>(`/projects/${enc(projectName)}/members/${enc(login)}`),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['projects', projectName] });
    },
  });
}

// -- Repos --

export function useRepos(projectName: string) {
  return useQuery({
    queryKey: ['projects', projectName, 'repos'],
    queryFn: () =>
      api.get<PaginatedResponse<Repo>>(`/projects/${enc(projectName)}/repos`),
    enabled: !!projectName,
    staleTime: 30_000,
  });
}

export function useRepo(projectName: string, repoType: string, repoName: string) {
  return useQuery({
    queryKey: ['projects', projectName, 'repos', repoType, repoName],
    queryFn: () =>
      api.get<Repo>(
        `/projects/${enc(projectName)}/repos/${enc(repoType)}/${enc(repoName)}`,
      ),
    enabled: !!projectName && !!repoType && !!repoName,
  });
}

export function useCreateRepo() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({
      projectName,
      data,
    }: {
      projectName: string;
      data: RepoCreate;
    }) => api.post<Repo>(`/projects/${enc(projectName)}/repos`, data),
    onSuccess: (_data, vars) => {
      // The project detail page renders tab counts from the project summary
      // (["projects", name]), repo cards from the project repo list
      // (["projects", name, "repos"]), and storage breakdown on the
      // dashboard. Invalidate all of them so create feels immediate.
      qc.invalidateQueries({ queryKey: ['projects', vars.projectName] });
      qc.invalidateQueries({ queryKey: ['projects', vars.projectName, 'repos'] });
      qc.invalidateQueries({ queryKey: ['dashboard'] });
    },
  });
}

export function usePatchRepo() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({
      projectName,
      repoType,
      repoName,
      data,
    }: {
      projectName: string;
      repoType: string;
      repoName: string;
      data: RepoPatch;
    }) => api.patch<Repo>(`/projects/${enc(projectName)}/repos/${enc(repoType)}/${enc(repoName)}`, data),
    onSuccess: (_data, vars) => {
      qc.invalidateQueries({
        queryKey: ['projects', vars.projectName, 'repos', vars.repoType, vars.repoName],
      });
      qc.invalidateQueries({ queryKey: ['projects', vars.projectName] });
      qc.invalidateQueries({ queryKey: ['projects', vars.projectName, 'repos'] });
    },
  });
}

export function useDeleteRepo() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({
      projectName,
      repoType,
      repoName,
    }: {
      projectName: string;
      repoType: string;
      repoName: string;
    }) => api.del<void>(`/projects/${enc(projectName)}/repos/${enc(repoType)}/${enc(repoName)}`),
    onSuccess: (_data, vars) => {
      qc.invalidateQueries({ queryKey: ['projects', vars.projectName] });
      qc.invalidateQueries({ queryKey: ['projects', vars.projectName, 'repos'] });
      qc.invalidateQueries({ queryKey: ['dashboard'] });
    },
  });
}

// -- Search --

export function useSearch(q: string, kind?: string, severity?: string, project?: string) {
  const params: Record<string, string> = {};
  if (q) params.q = q;
  if (kind) params.kind = kind;
  if (severity) params.severity = severity;
  if (project) params.project = project;

  return useQuery({
    queryKey: ['search', q, kind, severity, project],
    queryFn: () => api.get<SearchResponse>('/search', params),
    enabled: q.length > 0,
    staleTime: 10_000,
  });
}

// -- Maintenance --

export function useMaintenance() {
  // ME-06: public status endpoint (/maintenance/status) returns only the
  // enabled flag so the banner is visible to non-admin users. Admin-gated
  // details (toggled_by/toggled_at) live on /admin/maintenance.
  return useQuery({
    queryKey: ['maintenance'],
    queryFn: () => api.get<{ enabled: boolean }>('/maintenance/status'),
    staleTime: 30_000,
    retry: false,
  });
}

// -- Git --

export function useGitRefs(projectName: string, repoName: string) {
  return useQuery({
    queryKey: ['projects', projectName, 'repos', repoName, 'git', 'refs'],
    queryFn: () =>
      api.get<{ items: GitRef[] }>(
        `/projects/${enc(projectName)}/repos/git/${enc(repoName)}/refs`,
      ),
    enabled: !!projectName && !!repoName,
    staleTime: 30_000,
  });
}

export function useGitTree(
  projectName: string,
  repoName: string,
  ref: string,
  path: string,
) {
  return useQuery({
    queryKey: ['projects', projectName, 'repos', repoName, 'git', 'tree', ref, path],
    queryFn: () =>
      api.get<{ items: GitTreeEntry[] }>(
        // ref and path intentionally NOT encoded — both are multi-segment
        // (feature branches like `feat/x`, paths like `a/b/c.txt`). The
        // backend validates both at the handler layer.
        `/projects/${enc(projectName)}/repos/git/${enc(repoName)}/tree/${ref}/${path}`,
      ),
    enabled: !!projectName && !!repoName && !!ref,
    staleTime: 30_000,
  });
}

export function useGitBlob(
  projectName: string,
  repoName: string,
  ref: string,
  path: string,
) {
  return useQuery({
    queryKey: ['projects', projectName, 'repos', repoName, 'git', 'blob', ref, path],
    queryFn: () =>
      api.get<GitFileContent>(
        `/projects/${enc(projectName)}/repos/git/${enc(repoName)}/blob/${ref}/${path}`,
      ),
    enabled: !!projectName && !!repoName && !!ref && !!path,
    staleTime: 60_000,
  });
}

export function useGitCommits(
  projectName: string,
  repoName: string,
  ref: string,
  cursor?: string,
) {
  const params: Record<string, string> = {};
  if (cursor) params.cursor = cursor;
  return useQuery({
    queryKey: ['projects', projectName, 'repos', repoName, 'git', 'commits', ref, cursor],
    queryFn: () =>
      api.get<PaginatedResponse<GitCommit>>(
        `/projects/${enc(projectName)}/repos/git/${enc(repoName)}/commits/${ref}`,
        params,
      ),
    enabled: !!projectName && !!repoName && !!ref,
    staleTime: 30_000,
  });
}

export function useGitCommitDetail(
  projectName: string,
  repoName: string,
  sha: string,
) {
  return useQuery({
    queryKey: ['projects', projectName, 'repos', repoName, 'git', 'commit', sha],
    queryFn: () =>
      api.get<GitDiff>(
        `/projects/${enc(projectName)}/repos/git/${enc(repoName)}/commit/${enc(sha)}`,
      ),
    enabled: !!projectName && !!repoName && !!sha,
    staleTime: 120_000,
  });
}

export function useGitBlame(
  projectName: string,
  repoName: string,
  ref: string,
  path: string,
) {
  return useQuery({
    queryKey: ['projects', projectName, 'repos', repoName, 'git', 'blame', ref, path],
    queryFn: () =>
      api.get<GitBlame>(
        `/projects/${enc(projectName)}/repos/git/${enc(repoName)}/blame/${ref}/${path}`,
      ),
    enabled: !!projectName && !!repoName && !!ref && !!path,
    staleTime: 60_000,
  });
}

export function useGitCompare(
  projectName: string,
  repoName: string,
  base: string,
  head: string,
) {
  return useQuery({
    queryKey: ['projects', projectName, 'repos', repoName, 'git', 'compare', base, head],
    queryFn: () =>
      api.get<GitCompareResponse>(
        `/projects/${enc(projectName)}/repos/git/${enc(repoName)}/compare/${base}...${head}`,
      ),
    enabled: !!projectName && !!repoName && !!base && !!head,
    staleTime: 30_000,
  });
}

// -- Profile / Me --

export function useUpdateMe() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (data: MeUpdateRequest) =>
      api.patch<MeResponse>('/me', data),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['me'] }),
  });
}

export function useDeleteAccount() {
  return useMutation({
    mutationFn: () => api.del<void>('/me'),
  });
}

// -- API Keys --

export function useAPIKeys() {
  return useQuery({
    queryKey: ['me', 'api-keys'],
    queryFn: () => api.get<APIKey[]>('/me/api-keys'),
    staleTime: 30_000,
  });
}

export function useCreateAPIKey() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (data: APIKeyCreate) =>
      api.post<APIKeyCreateResponse>('/me/api-keys', data),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['me', 'api-keys'] }),
  });
}

export function useRevokeAPIKey() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: number) => api.del<void>(`/me/api-keys/${id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['me', 'api-keys'] }),
  });
}

// -- Project-owned API keys (omr_p_*, D-1) --

export function useProjectAPIKeys(projectName: string) {
  return useQuery({
    queryKey: ['projects', projectName, 'api-keys'],
    queryFn: () =>
      api.get<APIKey[]>(`/projects/${encodeURIComponent(projectName)}/api-keys`),
    staleTime: 30_000,
  });
}

export function useCreateProjectAPIKey(projectName: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (data: APIKeyCreate) =>
      api.post<APIKeyCreateResponse>(
        `/projects/${encodeURIComponent(projectName)}/api-keys`,
        data,
      ),
    onSuccess: () =>
      qc.invalidateQueries({ queryKey: ['projects', projectName, 'api-keys'] }),
  });
}

export function useRevokeProjectAPIKey(projectName: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: number) =>
      api.del<void>(
        `/projects/${encodeURIComponent(projectName)}/api-keys/${id}`,
      ),
    onSuccess: () =>
      qc.invalidateQueries({ queryKey: ['projects', projectName, 'api-keys'] }),
  });
}

// -- S3 Keys --

export function useS3Keys() {
  return useQuery({
    queryKey: ['me', 's3-keys'],
    queryFn: async () => {
      try {
        return await api.get<{ items: S3Key[] }>('/me/s3-keys');
      } catch {
        return { items: [] };
      }
    },
    staleTime: 30_000,
  });
}

export function useCreateS3Key() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (data: S3KeyCreate) =>
      api.post<S3KeyCreateResponse>('/me/s3-keys', data),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['me', 's3-keys'] }),
  });
}

export function useRevokeS3Key() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: number) => api.del<void>(`/me/s3-keys/${id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['me', 's3-keys'] }),
  });
}

// -- Project S3 buckets (walkthrough 2026-04-17, F-S3-C / F-S3-D) --

// Returns every non-deleted bucket owned by the named project, with live
// size_bytes + object_count. Uses the new REST endpoint added this session.
export function useProjectBuckets(projectName: string) {
  return useQuery({
    queryKey: ['projects', projectName, 'buckets'],
    queryFn: () =>
      api.get<ProjectBucket[]>(`/projects/${enc(projectName)}/s3-buckets/`),
    // Defend against a brief mount where useParams() returns '' —
    // firing the listing with an empty path segment resolves to
    // /projects//s3-buckets/, which chi serves as 404 and used to
    // surface as a noisy console error on bucket-detail loads where
    // this hook is transitively reached via query-cache warmups.
    enabled: !!projectName,
    staleTime: 10_000,
  });
}

export function useBucket(projectName: string, bucketName: string) {
  return useQuery({
    queryKey: ['projects', projectName, 'buckets', bucketName],
    queryFn: () =>
      api.get<BucketDetail>(
        `/projects/${enc(projectName)}/s3-buckets/${enc(bucketName)}`,
      ),
    enabled: !!projectName && !!bucketName,
    staleTime: 10_000,
  });
}

export function useBucketObjects(
  projectName: string,
  bucketName: string,
  opts?: { prefix?: string; marker?: string; limit?: number },
) {
  return useQuery({
    queryKey: [
      'projects',
      projectName,
      'buckets',
      bucketName,
      'objects',
      opts?.prefix ?? '',
      opts?.marker ?? '',
      opts?.limit ?? 0,
    ],
    queryFn: () => {
      const params: Record<string, string> = {};
      if (opts?.prefix) params.prefix = opts.prefix;
      if (opts?.marker) params.marker = opts.marker;
      if (opts?.limit) params.limit = String(opts.limit);
      return api.get<BucketObjectsPage>(
        `/projects/${enc(projectName)}/s3-buckets/${enc(bucketName)}/objects`,
        params,
      );
    },
    enabled: !!projectName && !!bucketName,
    staleTime: 10_000,
  });
}

export function useCreateBucket(projectName: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (data: BucketCreate) =>
      api.post<ProjectBucket>(
        `/projects/${enc(projectName)}/s3-buckets/`,
        data,
      ),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['projects', projectName, 'buckets'] });
      qc.invalidateQueries({ queryKey: ['projects', projectName] });
      qc.invalidateQueries({ queryKey: ['dashboard'] });
      qc.invalidateQueries({ queryKey: ['dashboard', 'storage'] });
    },
  });
}

export function useDeleteBucket(projectName: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (bucketName: string) =>
      api.del<void>(`/projects/${enc(projectName)}/s3-buckets/${enc(bucketName)}`),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['projects', projectName, 'buckets'] });
      qc.invalidateQueries({ queryKey: ['projects', projectName] });
      qc.invalidateQueries({ queryKey: ['dashboard'] });
      qc.invalidateQueries({ queryKey: ['dashboard', 'storage'] });
    },
  });
}

// -- Docker pull-external (Phase 8 / plan 08-03) ---------------------------

/**
 * usePullExternal — enqueues a Docker clone-external job via
 * POST /api/v1/projects/{name}/repos/docker/{repo}/pull-external. Body
 * fields match the Go wire shape in
 * `internal/protocol/oci/pull_external.go:PullExternalRequest`:
 *   - src_image (required)           e.g. "docker.io/library/nginx:1.27"
 *   - dst_tag (optional)             retag under the destination repo
 *   - cred_id (optional)             ID of a stored upstream credential
 *   - src_username / src_password    inline creds, cleartext in v1.1
 *
 * Returns { job_id } — the CloneImageDialog feeds that into
 * useJobProgress to render the live progress bar. On success the
 * repo's content + scans caches are invalidated so the newly-cloned
 * image appears in the tag list as soon as the job finishes.
 */
export function usePullExternal(projectName: string, repoName: string) {
  const qc = useQueryClient();
  return useMutation<PullExternalResponse, Error, PullExternalRequest>({
    mutationFn: (body) =>
      api.post<PullExternalResponse>(
        `/projects/${enc(projectName)}/repos/docker/${enc(repoName)}/pull-external`,
        body,
      ),
    onSuccess: () => {
      // Invalidate lazily — the pool hasn't finished the job yet.
      // CloneImageDialog re-invalidates these keys explicitly when the
      // polled job flips to status=done, which is when the new tag
      // actually shows up in /content. Keeping this invalidation here
      // is a best-effort for any fast-path (<500ms) jobs that land
      // done before the poll observes them.
      qc.invalidateQueries({
        queryKey: ['repo-content', projectName, 'docker', repoName],
      });
    },
  });
}

/**
 * useSyncRepo — Phase 8 Plan 04 (MIRROR-19). POST /sync on a mirror
 * repo with an empty body enqueues a sync job that reads its config
 * from the repo row (mirror_upstream_url, mirror_filter_json,
 * mirror_cred_id, scan_on_sync) — see
 * internal/httpx/sync_rest.go's 3-way branch. The 409
 * sync.sync_already_running envelope surfaces via ErrorEnvelopeRenderer
 * when a prior job is still in flight (backend CountRepoInflight guard,
 * plan 08-01 T-08-01-04).
 *
 * Used by SyncNowButton on AptRepoPage / RpmRepoPage / PypiRepoPage /
 * HelmRepoPage. Invalidation strategy mirrors usePullExternal — the
 * polled job (useJobProgress) invalidates /content + /scans on status
 * flip to "done", so we only kick off a best-effort invalidation here
 * for fast-path (<500ms) jobs.
 */
export function useSyncRepo(
  projectName: string,
  repoType: string,
  repoName: string,
) {
  const qc = useQueryClient();
  return useMutation<SyncEnqueueResponse, Error, void>({
    mutationFn: () =>
      // POST with NO body — a non-empty body would trip the backend's
      // mirror-overrides check (see internal/httpx/sync_rest.go:172). The
      // trailing `{}` sent by earlier revisions JSON-encoded to 2 bytes,
      // which is not whitespace, and produced a 400 sync.mirror_overrides_not_allowed.
      api.post<SyncEnqueueResponse>(
        `/projects/${enc(projectName)}/repos/${enc(repoType)}/${enc(repoName)}/sync`,
      ),
    onSuccess: () => {
      qc.invalidateQueries({
        queryKey: ['repo-content', projectName, repoType, repoName],
      });
    },
  });
}

/**
 * useUpstreamCreds — GET /projects/{name}/upstream-creds/ project-scoped
 * credential list. Returns a secret-free projection (id/host/kind/
 * username/timestamps) per `internal/api/upstream_creds.go:
 * upstreamCredResponse`.
 *
 * Consumers: CloneImageDialog credential picker and (plan 08-05)
 * ProjectSettingsPage upstream-creds tab. The endpoint is skipped on
 * the backend when no AEAD key is materialised — callers should handle
 * an empty list gracefully (the picker just renders the "no creds"
 * fallback in that case).
 */
export function useUpstreamCreds(projectName: string) {
  return useQuery({
    queryKey: ['projects', projectName, 'upstream-creds'],
    queryFn: () =>
      api.get<UpstreamCred[]>(`/projects/${enc(projectName)}/upstream-creds/`),
    enabled: !!projectName,
    staleTime: 30_000,
    // If the endpoint is unmounted (no AEAD) the fetch returns 404 —
    // treat that as "no creds" rather than a hard error so the picker
    // just degrades gracefully.
    retry: false,
  });
}

/**
 * useCreateUpstreamCred — POST /projects/{name}/upstream-creds (Phase 8
 * Plan 05 / MIRROR-22). On success invalidates the list cache so both
 * the CloneImageDialog and MirrorConfigSection cred pickers, plus the
 * new UpstreamCredsTab table, all refetch.
 *
 * Response is an UpstreamCred (secret-free projection — password/token
 * never round-trip). Error is ApiError from api.post, which the caller
 * surfaces via ErrorEnvelopeRenderer.
 */
export function useCreateUpstreamCred(projectName: string) {
  const qc = useQueryClient();
  return useMutation<UpstreamCred, Error, UpstreamCredCreate>({
    mutationFn: (body) =>
      api.post<UpstreamCred>(
        `/projects/${enc(projectName)}/upstream-creds/`,
        body,
      ),
    onSuccess: () => {
      qc.invalidateQueries({
        queryKey: ['projects', projectName, 'upstream-creds'],
      });
    },
  });
}

/**
 * usePatchUpstreamCred — PATCH /projects/{name}/upstream-creds/{id}.
 *
 * Blank-preserves-existing contract (T-08-05-03): the caller MUST omit
 * `password` / `token` keys from the body when the operator leaves the
 * edit-form fields blank. The backend's handleUpdateUpstreamCred reads
 * the cred request struct with `omitempty` and treats unset strings as
 * "keep existing" — but the safe client idiom is to strip the key
 * entirely so no future backend change can reinterpret `""`.
 */
export function usePatchUpstreamCred(projectName: string, credId: number) {
  const qc = useQueryClient();
  return useMutation<UpstreamCred, Error, UpstreamCredPatch>({
    mutationFn: (body) =>
      api.patch<UpstreamCred>(
        `/projects/${enc(projectName)}/upstream-creds/${credId}`,
        body,
      ),
    onSuccess: () => {
      qc.invalidateQueries({
        queryKey: ['projects', projectName, 'upstream-creds'],
      });
    },
  });
}

/**
 * useDeleteUpstreamCred — DELETE /projects/{name}/upstream-creds/{id}
 * returns 204. Mirror repos that reference the deleted cred have their
 * `mirror_cred_id` set to NULL by the schema's `ON DELETE SET NULL`
 * (plan 08-01); the next sync on those repos fails with a
 * `credential missing` envelope rather than silently continuing.
 * The UI surfaces this consequence in a confirmation dialog before the
 * mutation fires (UpstreamCredsTab delete handler).
 */
export function useDeleteUpstreamCred(projectName: string, credId: number) {
  const qc = useQueryClient();
  return useMutation<void, Error, void>({
    mutationFn: () =>
      api.del<void>(
        `/projects/${enc(projectName)}/upstream-creds/${credId}`,
      ),
    onSuccess: () => {
      qc.invalidateQueries({
        queryKey: ['projects', projectName, 'upstream-creds'],
      });
      // A deleted cred may have been referenced by a mirror repo — the
      // backend sets mirror_cred_id = NULL on those rows (ON DELETE
      // SET NULL). Invalidate the project repo list so the Mirror
      // config card in RepoSettingsTab reflects the new null state.
      qc.invalidateQueries({
        queryKey: ['projects', projectName, 'repos'],
      });
    },
  });
}
