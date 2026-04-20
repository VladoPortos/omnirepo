/**
 * useJobProgress — TanStack Query polling hook for Phase 8 / plan 08-03.
 *
 * Polls the per-repo sync-job detail endpoint (GET
 * /api/v1/projects/{name}/repos/{type}/{repo}/sync-jobs/{id}) every 500 ms
 * while status IN {pending, running} and short-circuits `refetchInterval`
 * to `false` on {done, failed}. Reshapes the JobDetail response into a
 * UI-friendly `JobProgress` shape with a pre-computed percent (null when
 * total_bytes is 0 — Helm ships step-based progress per D-11).
 *
 * IMPORTANT endpoint delta from plan 08-03's sketch:
 *   Plan referenced `GET /api/v1/jobs/{id}` (no such endpoint exists on
 *   the v1.1 server). The authoritative endpoint lives under the
 *   per-repo scope emitted by plan 08-02 (`internal/api/repos_list.go:
 *   handleGetSyncJob`). The hook's signature therefore accepts
 *   (projectName, repoType, repoName, jobId) so the URL can be built
 *   against the real wire surface. Idle behaviour is unchanged.
 *
 * TanStack Query v5 notes:
 *   `refetchInterval` accepts `(query) => number | false`. Polling stops
 *   when it returns `false`. We return `500` while active, `false` on
 *   terminal status. staleTime is kept at 0 so the first render hits
 *   the network immediately (cold-start).
 */

import { useQuery } from '@tanstack/react-query';
import { api, localEnvelope, type ApiErrorEnvelope } from '@/api/client';
import type { JobDetail, JobStatus } from '@/api/types';

export const POLL_INTERVAL_MS = 500;

export interface JobProgress {
  status: JobStatus;
  progressBytes: number;
  totalBytes: number;
  currentStep: string;
  /**
   * Quick task 260420-d03 (D-03 closure): files newly added during the
   * sync. 0 for running jobs; the success pill renders
   * "Sync complete · N files · X MB" when this is > 0 and totalBytes > 0.
   */
  filesSynced: number;
  percent: number | null;
  error: ApiErrorEnvelope | null;
  isPolling: boolean;
}

/**
 * Idle JobProgress returned while jobId is null (no job to observe yet)
 * or before the first poll response lands. Exported so callers can
 * compare against a stable reference if needed.
 */
export const idleJobProgress: JobProgress = {
  status: 'pending',
  progressBytes: 0,
  totalBytes: 0,
  currentStep: '',
  filesSynced: 0,
  percent: null,
  error: null,
  isPolling: false,
};

/**
 * computeJobProgress — pure reshaping of a JobDetail wire row into the
 * UI's JobProgress shape. Exported for unit testing so the percent-edge
 * logic can be asserted without React or DOM. Called by useJobProgress
 * on every render with TanStack's cached data.
 *
 * Defensive coercion (threat T-08-03-04): numeric fields run through
 * `Number(x) || 0` so a malformed upstream progress JSON doesn't crash
 * the bar. current_step goes through `String(x ?? '')`.
 */
export function computeJobProgress(detail: JobDetail | undefined): JobProgress {
  if (!detail) return idleJobProgress;
  const progressBytes = Number(detail.progress_bytes) || 0;
  const totalBytes = Number(detail.total_bytes) || 0;
  const currentStep = String(detail.current_step ?? '');
  const filesSynced = Number(detail.files_synced) || 0;
  const percent =
    totalBytes > 0 ? Math.round((progressBytes / totalBytes) * 100) : null;
  const status = detail.status;
  const error =
    status === 'failed' && detail.last_error
      ? localEnvelope(detail.last_error, {
          class: 'transient',
          code: 'job.failed',
        })
      : null;
  return {
    status,
    progressBytes,
    totalBytes,
    currentStep,
    filesSynced,
    percent,
    error,
    isPolling: status === 'pending' || status === 'running',
  };
}

/**
 * pollingDecision — pure function extracted from the hook's
 * refetchInterval callback so it's unit-testable without TanStack's
 * query object. Returns the delay to use for the next poll, or `false`
 * to stop polling. First-run (no data yet) also polls after 500 ms.
 */
export function pollingDecision(detail: JobDetail | undefined): number | false {
  if (!detail) return POLL_INTERVAL_MS;
  if (detail.status === 'done' || detail.status === 'failed') return false;
  return POLL_INTERVAL_MS;
}

/**
 * useJobProgress — TanStack hook. Pass jobId=null to disable the query
 * entirely (no network fetch is issued; idleJobProgress is returned).
 * The (projectName, repoType, repoName) triple is required to build
 * the per-repo sync-job URL — the backend rejects cross-scope requests
 * with 404.
 */
export function useJobProgress(
  projectName: string,
  repoType: string,
  repoName: string,
  jobId: number | null,
): JobProgress {
  const enabled = jobId !== null && !!projectName && !!repoType && !!repoName;
  const q = useQuery<JobDetail>({
    queryKey: ['sync-job', projectName, repoType, repoName, jobId],
    queryFn: () =>
      api.get<JobDetail>(
        `/projects/${encodeURIComponent(projectName)}/repos/${encodeURIComponent(repoType)}/${encodeURIComponent(repoName)}/sync-jobs/${jobId}`,
      ),
    enabled,
    // v5 signature: callback receives the query object, NOT the data
    // value. The pure decision helper below is unit-tested.
    refetchInterval: (query) => pollingDecision(query.state.data),
    staleTime: 0,
  });

  if (!enabled) return idleJobProgress;
  return computeJobProgress(q.data);
}
