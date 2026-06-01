/**
 * useJobProgress — TanStack Query polling hook.
 *
 * Polls the per-repo sync-job detail endpoint (GET
 * /api/v1/projects/{name}/repos/{type}/{repo}/sync-jobs/{id}) every 500 ms
 * while status IN {pending, running} and short-circuits `refetchInterval`
 * to `false` on {done, failed}. Reshapes the JobDetail response into a
 * UI-friendly `JobProgress` shape with a pre-computed percent (null when
 * total_bytes is 0 — Helm ships step-based progress).
 *
 * IMPORTANT endpoint note:
 *   There is no `GET /api/v1/jobs/{id}` endpoint on the server. The
 *   authoritative endpoint lives under the per-repo scope
 *   (`internal/api/repos_list.go: handleGetSyncJob`). The hook's
 *   signature therefore accepts (projectName, repoType, repoName, jobId)
 *   so the URL can be built against the real wire surface. Idle
 *   behaviour is unchanged.
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
   * Files newly added during the sync. 0 for running jobs; the success
   * pill renders "Sync complete · N files · X MB" when this is > 0 and
   * totalBytes > 0.
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
 * Defensive coercion: numeric fields run through `Number(x) || 0` so a
 * malformed upstream progress JSON doesn't crash the bar. current_step
 * goes through `String(x ?? '')`.
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
  const attempts = Number(detail.attempts) || 0;
  const lastError = detail.last_error ?? '';
  // The job runner retries transient sync failures
  // up to MaxAttempts=5 (backoff 1m/5m/30m/30m/30m) before marking the row
  // `failed`. Between attempts the row stays status=pending with
  // attempts>=1 and last_error set. Pre-fix we treated that as a normal
  // in-flight state and kept the progress bar pinned on "Preparing…" for
  // up to 96 minutes while the bogus-upstream mirror churned DNS lookups.
  // Surface it as a proper (transient-class) error envelope so the UI
  // renders the "sync failed, retrying" pill the spec expects after the
  // first attempt. `isPolling` stays true through retry-backoff so the
  // bar flips back to "running" if a later attempt progresses.
  const inRetryBackoff =
    status === 'pending' && attempts >= 1 && lastError !== '';
  const error =
    (status === 'failed' && lastError) || inRetryBackoff
      ? localEnvelope(lastError, {
          class: 'transient',
          code: inRetryBackoff ? 'job.retrying' : 'job.failed',
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
 *
 * When the sync-job endpoint surfaces a 4xx
 * (repo deleted underneath, auth dropped, job id invalid), stop
 * polling instead of looping 2×/sec forever. 5xx still retries once
 * so a transient outage self-heals. The caller passes `error` through
 * from TanStack's query.state.error; `undefined` means "no error".
 */
export interface PollingDecisionInput {
  detail: JobDetail | undefined;
  /** TanStack query error, if any. We only inspect numeric .status. */
  error?: { status?: number } | null;
}

export function pollingDecision(
  input: JobDetail | undefined | PollingDecisionInput,
): number | false {
  const data =
    input && typeof input === 'object' && 'detail' in input
      ? (input as PollingDecisionInput).detail
      : (input as JobDetail | undefined);
  const err =
    input && typeof input === 'object' && 'error' in input
      ? (input as PollingDecisionInput).error ?? null
      : null;
  if (err && typeof err.status === 'number' && err.status >= 400 && err.status < 500) {
    // 4xx — repo deleted / no longer authorized / job id gone. No point
    // hammering the endpoint; user will re-open the page to re-arm.
    return false;
  }
  if (!data) return POLL_INTERVAL_MS;
  if (data.status === 'done' || data.status === 'failed') return false;
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
    refetchInterval: (query) =>
      pollingDecision({
        detail: query.state.data,
        // Surface any HTTP-shaped error (.status set by api.get on non-2xx)
        // so 4xx (repo deleted mid-sync) halts polling.
        error: query.state.error as { status?: number } | null,
      }),
    // Don't keep the query alive across repo deletions. When the UI
    // unmounts the page, stop retrying on network errors immediately —
    // matching the refetchInterval short-circuit for 4xx.
    retry: (failureCount, error) => {
      const status = (error as { status?: number })?.status;
      if (typeof status === 'number' && status >= 400 && status < 500) {
        return false;
      }
      return failureCount < 2;
    },
    staleTime: 0,
  });

  if (!enabled) return idleJobProgress;
  return computeJobProgress(q.data);
}
