/**
 * TanStack Query hooks for OmniRepo REST API.
 * Query keys structured as ['entity', ...params] per D-36.
 */

import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { api, ApiError } from './client';
import type {
  MeResponse,
  MeUpdateRequest,
  LoginRequest,
  LoginResponse,
  SetupStatusResponse,
  SetupSuperAdminRequest,
  SetupSuperAdminResponse,
  RepoContentEntry,
  ProjectListItem,
  ProjectDetail,
  ProjectCreate,
  ProjectCreateResponse,
  ActivityItem,
  Repo,
  RepoCreate,
  RepoPatch,
  DashboardResponse,
  DashboardStorageResponse,
  SearchResponse,
  PaginatedResponse,
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
} from './types';

// -- Repo-level scans list (populates the Scan Results tab on every repo page) --

export function useRepoScans(
  projectName: string,
  repoType: string,
  repoName: string,
  opts?: { status?: ScanStatus; limit?: number },
) {
  return useQuery({
    queryKey: ['repo-scans', projectName, repoType, repoName, opts?.status ?? '', opts?.limit ?? 100],
    queryFn: () => {
      const params: Record<string, string> = {};
      if (opts?.status) params.status = opts.status;
      if (opts?.limit != null) params.limit = String(opts.limit);
      return api.get<Scan[]>(
        `/projects/${projectName}/repos/${repoType}/${repoName}/scans`,
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

// -- Repo content (listing artifacts uploaded to a repo) --

export function useRepoContent(
  projectName: string,
  repoType: string,
  repoName: string,
  opts?: { limit?: number; offset?: number },
) {
  return useQuery({
    queryKey: ['repo-content', projectName, repoType, repoName, opts?.limit ?? 100, opts?.offset ?? 0],
    queryFn: () => {
      const params: Record<string, string> = {};
      if (opts?.limit != null) params.limit = String(opts.limit);
      if (opts?.offset != null) params.offset = String(opts.offset);
      return api.get<RepoContentEntry[]>(
        `/projects/${projectName}/repos/${repoType}/${repoName}/content`,
        params,
      );
    },
    staleTime: 15_000,
  });
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
    queryFn: () => api.get<ProjectDetail>(`/projects/${name}`),
    enabled: !!name,
  });
}

export function useProjectActivity(name: string) {
  return useQuery({
    queryKey: ['projects', name, 'activity'],
    queryFn: () => api.get<{ items: ActivityItem[] }>(`/projects/${name}/activity`),
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
    mutationFn: (name: string) => api.del<void>(`/projects/${name}`),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['projects'] });
      qc.invalidateQueries({ queryKey: ['dashboard'] });
    },
  });
}

// -- Repos --

export function useRepos(projectName: string) {
  return useQuery({
    queryKey: ['projects', projectName, 'repos'],
    queryFn: () =>
      api.get<PaginatedResponse<Repo>>(`/projects/${projectName}/repos`),
    enabled: !!projectName,
    staleTime: 30_000,
  });
}

export function useRepo(projectName: string, repoType: string, repoName: string) {
  return useQuery({
    queryKey: ['projects', projectName, 'repos', repoType, repoName],
    queryFn: () =>
      api.get<Repo>(`/projects/${projectName}/repos/${repoType}/${repoName}`),
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
    }) => api.post<Repo>(`/projects/${projectName}/repos`, data),
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
    }) => api.patch<Repo>(`/projects/${projectName}/repos/${repoType}/${repoName}`, data),
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
    }) => api.del<void>(`/projects/${projectName}/repos/${repoType}/${repoName}`),
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
        `/projects/${projectName}/repos/git/${repoName}/refs`,
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
        `/projects/${projectName}/repos/git/${repoName}/tree/${ref}/${path}`,
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
        `/projects/${projectName}/repos/git/${repoName}/blob/${ref}/${path}`,
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
        `/projects/${projectName}/repos/git/${repoName}/commits/${ref}`,
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
        `/projects/${projectName}/repos/git/${repoName}/commit/${sha}`,
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
        `/projects/${projectName}/repos/git/${repoName}/blame/${ref}/${path}`,
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
        `/projects/${projectName}/repos/git/${repoName}/compare/${base}...${head}`,
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
      api.get<ProjectBucket[]>(`/projects/${projectName}/s3-buckets/`),
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
        `/projects/${projectName}/s3-buckets/${bucketName}`,
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
        `/projects/${projectName}/s3-buckets/${bucketName}/objects`,
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
        `/projects/${projectName}/s3-buckets/`,
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
      api.del<void>(`/projects/${projectName}/s3-buckets/${bucketName}`),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['projects', projectName, 'buckets'] });
      qc.invalidateQueries({ queryKey: ['projects', projectName] });
      qc.invalidateQueries({ queryKey: ['dashboard'] });
      qc.invalidateQueries({ queryKey: ['dashboard', 'storage'] });
    },
  });
}
