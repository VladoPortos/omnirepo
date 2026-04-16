/**
 * TanStack Query hooks for OmniRepo REST API.
 * Query keys structured as ['entity', ...params] per D-36.
 */

import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { api } from './client';
import type {
  MeResponse,
  LoginRequest,
  LoginResponse,
  ProjectListItem,
  ProjectDetail,
  ProjectCreate,
  ProjectCreateResponse,
  ActivityItem,
  Repo,
  RepoCreate,
  RepoPatch,
  DashboardResponse,
  SearchResponse,
  MaintenanceStatus,
  PaginatedResponse,
  GitTreeEntry,
  GitFileContent,
  GitCommit,
  GitDiff,
  GitBlame,
  GitRef,
  GitCompareResponse,
} from './types';

// -- Auth / Me --

export function useMe() {
  return useQuery({
    queryKey: ['me'],
    queryFn: () => api.get<MeResponse>('/me'),
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
    onSuccess: () => qc.invalidateQueries({ queryKey: ['projects'] }),
  });
}

export function useDeleteProject() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (name: string) => api.del<void>(`/projects/${name}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['projects'] }),
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

export function useRepo(projectName: string, repoName: string) {
  return useQuery({
    queryKey: ['projects', projectName, 'repos', repoName],
    queryFn: () =>
      api.get<Repo>(`/projects/${projectName}/repos/${repoName}`),
    enabled: !!projectName && !!repoName,
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
    onSuccess: (_data, vars) =>
      qc.invalidateQueries({
        queryKey: ['projects', vars.projectName, 'repos'],
      }),
  });
}

export function usePatchRepo() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({
      projectName,
      repoName,
      data,
    }: {
      projectName: string;
      repoName: string;
      data: RepoPatch;
    }) => api.patch<Repo>(`/projects/${projectName}/repos/${repoName}`, data),
    onSuccess: (_data, vars) =>
      qc.invalidateQueries({
        queryKey: ['projects', vars.projectName, 'repos', vars.repoName],
      }),
  });
}

export function useDeleteRepo() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({
      projectName,
      repoName,
    }: {
      projectName: string;
      repoName: string;
    }) => api.del<void>(`/projects/${projectName}/repos/${repoName}`),
    onSuccess: (_data, vars) =>
      qc.invalidateQueries({
        queryKey: ['projects', vars.projectName, 'repos'],
      }),
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
  return useQuery({
    queryKey: ['maintenance'],
    queryFn: () => api.get<MaintenanceStatus>('/admin/maintenance'),
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
        `/projects/${projectName}/repos/${repoName}/git/refs`,
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
        `/projects/${projectName}/repos/${repoName}/git/tree/${ref}/${path}`,
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
        `/projects/${projectName}/repos/${repoName}/git/blob/${ref}/${path}`,
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
        `/projects/${projectName}/repos/${repoName}/git/commits/${ref}`,
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
        `/projects/${projectName}/repos/${repoName}/git/commit/${sha}`,
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
        `/projects/${projectName}/repos/${repoName}/git/blame/${ref}/${path}`,
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
        `/projects/${projectName}/repos/${repoName}/git/compare/${base}...${head}`,
      ),
    enabled: !!projectName && !!repoName && !!base && !!head,
    staleTime: 30_000,
  });
}
