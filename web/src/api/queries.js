/**
 * TanStack Query hooks for OmniRepo REST API.
 * Query keys structured as ['entity', ...params] per D-36.
 */
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { api } from './client';
// -- Auth / Me --
export function useMe() {
    return useQuery({
        queryKey: ['me'],
        queryFn: () => api.get('/me'),
        staleTime: 60_000,
        retry: false,
    });
}
export function useLogin() {
    const qc = useQueryClient();
    return useMutation({
        mutationFn: (creds) => api.post('/auth/login', creds),
        onSuccess: () => qc.invalidateQueries({ queryKey: ['me'] }),
    });
}
export function useLogout() {
    const qc = useQueryClient();
    return useMutation({
        mutationFn: () => api.post('/auth/logout'),
        onSuccess: () => {
            qc.clear();
            window.location.href = '/login';
        },
    });
}
export function useChangePassword() {
    const qc = useQueryClient();
    return useMutation({
        mutationFn: (data) => api.post('/auth/change-password', { current: data.current, new: data.new_password }),
        onSuccess: () => qc.invalidateQueries({ queryKey: ['me'] }),
    });
}
// -- Dashboard --
export function useDashboard() {
    return useQuery({
        queryKey: ['dashboard'],
        queryFn: () => api.get('/dashboard'),
        staleTime: 15_000,
    });
}
// -- Projects --
export function useProjects() {
    return useQuery({
        queryKey: ['projects'],
        queryFn: () => api.get('/projects'),
        staleTime: 30_000,
    });
}
export function useProject(name) {
    return useQuery({
        queryKey: ['projects', name],
        queryFn: () => api.get(`/projects/${name}`),
        enabled: !!name,
    });
}
export function useCreateProject() {
    const qc = useQueryClient();
    return useMutation({
        mutationFn: (data) => api.post('/projects', data),
        onSuccess: () => qc.invalidateQueries({ queryKey: ['projects'] }),
    });
}
export function useDeleteProject() {
    const qc = useQueryClient();
    return useMutation({
        mutationFn: (name) => api.del(`/projects/${name}`),
        onSuccess: () => qc.invalidateQueries({ queryKey: ['projects'] }),
    });
}
// -- Repos --
export function useRepos(projectName) {
    return useQuery({
        queryKey: ['projects', projectName, 'repos'],
        queryFn: () => api.get(`/projects/${projectName}/repos`),
        enabled: !!projectName,
        staleTime: 30_000,
    });
}
export function useRepo(projectName, repoName) {
    return useQuery({
        queryKey: ['projects', projectName, 'repos', repoName],
        queryFn: () => api.get(`/projects/${projectName}/repos/${repoName}`),
        enabled: !!projectName && !!repoName,
    });
}
export function useCreateRepo() {
    const qc = useQueryClient();
    return useMutation({
        mutationFn: ({ projectName, data, }) => api.post(`/projects/${projectName}/repos`, data),
        onSuccess: (_data, vars) => qc.invalidateQueries({
            queryKey: ['projects', vars.projectName, 'repos'],
        }),
    });
}
export function usePatchRepo() {
    const qc = useQueryClient();
    return useMutation({
        mutationFn: ({ projectName, repoName, data, }) => api.patch(`/projects/${projectName}/repos/${repoName}`, data),
        onSuccess: (_data, vars) => qc.invalidateQueries({
            queryKey: ['projects', vars.projectName, 'repos', vars.repoName],
        }),
    });
}
export function useDeleteRepo() {
    const qc = useQueryClient();
    return useMutation({
        mutationFn: ({ projectName, repoName, }) => api.del(`/projects/${projectName}/repos/${repoName}`),
        onSuccess: (_data, vars) => qc.invalidateQueries({
            queryKey: ['projects', vars.projectName, 'repos'],
        }),
    });
}
// -- Search --
export function useSearch(q, kind, severity, project) {
    const params = {};
    if (q)
        params.q = q;
    if (kind)
        params.kind = kind;
    if (severity)
        params.severity = severity;
    if (project)
        params.project = project;
    return useQuery({
        queryKey: ['search', q, kind, severity, project],
        queryFn: () => api.get('/search', params),
        enabled: q.length > 0,
        staleTime: 10_000,
    });
}
// -- Maintenance --
export function useMaintenance() {
    return useQuery({
        queryKey: ['maintenance'],
        queryFn: () => api.get('/admin/maintenance'),
        staleTime: 30_000,
        retry: false,
    });
}
