/**
 * SyncNowButton — Phase 8 Plan 04 (MIRROR-19, D-17).
 *
 * Single "Sync now" affordance rendered at the top of each mirror-aware
 * protocol page (AptRepoPage, RpmRepoPage, PypiRepoPage, HelmRepoPage).
 * Wraps useSyncRepo (POST /sync with empty body per D-06) + useJobProgress
 * (same hook as CloneImageDialog — plan 08-03) so the operator sees a
 * byte-level progress line while the sync runs.
 *
 * Pattern matches CloneImageDialog's progress phase but simpler: no form
 * (the mirror config is already on the repo row), no retag, no cred
 * picker. Just a button + inline progress + ErrorEnvelopeRenderer.
 *
 * Disable rules:
 *   - Button disabled while the mutation is pending (prevents rapid
 *     double-fire)
 *   - Button disabled while `isPolling` — backend also enforces
 *     concurrency via 409 sync.sync_already_running, but we don't
 *     want to generate those 409s from our own UI double-clicks.
 *
 * The backend emits
 *   POST .../repos/{type}/{repo}/sync → 202 { job_id, kind }
 * (see internal/httpx/sync_rest.go). The GET endpoint the progress hook
 * polls is `/projects/{name}/repos/{type}/{repo}/sync-jobs/{id}` (per-repo
 * scope — same URL shape plan 08-03 proved).
 */

import { useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import { useQueryClient } from '@tanstack/react-query';
import { Loader2, RefreshCw, Settings } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Progress } from '@/components/ui/progress';
import { ErrorEnvelopeRenderer } from '@/components/common/ErrorEnvelope';
import { envelopeFromError, type ApiErrorEnvelope } from '@/api/client';
import { useSyncRepo } from '@/api/queries';
import { useJobProgress } from '@/hooks/useJobProgress';
import { formatBytes } from '@/lib/format';

export interface SyncNowButtonProps {
  projectName: string;
  repoType: string;
  repoName: string;
  upstreamUrl: string;
  /** Short human summary of the current filter. E.g.
   *  "focal · main, universe · amd64" for APT. Optional; when absent
   *  we just display the upstream URL. */
  filterSummary?: string;
}

export function SyncNowButton({
  projectName,
  repoType,
  repoName,
  upstreamUrl,
  filterSummary,
}: SyncNowButtonProps) {
  const qc = useQueryClient();
  const mutation = useSyncRepo(projectName, repoType, repoName);
  const [jobId, setJobId] = useState<number | null>(null);
  const progress = useJobProgress(projectName, repoType, repoName, jobId);
  const [mutationError, setMutationError] =
    useState<ApiErrorEnvelope | null>(null);

  const isPolling = progress.isPolling;
  const disabled = mutation.isPending || isPolling;

  // When the polled job terminates, invalidate content + repo caches
  // so the UI reflects newly-synced artifacts without a manual refresh.
  useEffect(() => {
    if (jobId == null) return;
    if (progress.status === 'done') {
      qc.invalidateQueries({
        queryKey: ['repo-content', projectName, repoType, repoName],
      });
      qc.invalidateQueries({
        queryKey: ['repo-scans', projectName, repoType, repoName],
      });
      qc.invalidateQueries({
        queryKey: ['projects', projectName, 'repos', repoType, repoName],
      });
    }
  }, [progress.status, jobId, projectName, repoType, repoName, qc]);

  const handleClick = () => {
    setMutationError(null);
    mutation.mutate(undefined, {
      onSuccess: (resp) => {
        setJobId(resp.job_id);
      },
      onError: (err) => {
        setMutationError(envelopeFromError(err, 'Failed to start sync.'));
      },
    });
  };

  const progressPercent = progress.percent ?? 0;
  const progressLine = (() => {
    const step = progress.currentStep || 'Preparing…';
    if (progress.totalBytes > 0) {
      const frac = `${formatBytes(progress.progressBytes)} / ${formatBytes(progress.totalBytes)}`;
      const pct = progress.percent == null ? '?' : `${progress.percent}`;
      return `${step} · ${frac} · ${pct}%`;
    }
    // Helm step-based progress — total_bytes==0, step carries "chart N of M".
    if (progress.progressBytes > 0) {
      return `${step} · ${formatBytes(progress.progressBytes)} transferred`;
    }
    return step;
  })();

  return (
    <div className="rounded-lg border bg-muted/20 p-3 space-y-2">
      <div className="flex items-start justify-between gap-3">
        <div className="flex-1 text-sm min-w-0">
          <p className="font-semibold">Mirror of upstream</p>
          <p className="text-xs text-muted-foreground font-mono break-all">
            {upstreamUrl}
          </p>
          {filterSummary && (
            <p className="text-xs text-muted-foreground mt-1">
              Filter: {filterSummary}
            </p>
          )}
        </div>
        <div className="flex items-center gap-2 shrink-0">
          <Button
            variant="outline"
            size="sm"
            nativeButton={false}
            render={
              <Link
                to={`/projects/${encodeURIComponent(projectName)}/${encodeURIComponent(repoType)}/${encodeURIComponent(repoName)}/settings`}
              />
            }
            title="Edit mirror filter / credential / scan settings"
          >
            <Settings className="mr-1.5 size-3.5" />
            Settings
          </Button>
          <Button
            onClick={handleClick}
            disabled={disabled}
            size="sm"
            title={
              isPolling
                ? 'A sync is already in progress for this mirror'
                : 'Run the mirror sync now'
            }
          >
            {disabled ? (
              <Loader2 className="mr-1.5 size-3.5 animate-spin" />
            ) : (
              <RefreshCw className="mr-1.5 size-3.5" />
            )}
            {disabled ? 'Syncing…' : 'Sync now'}
          </Button>
        </div>
      </div>

      {jobId !== null && isPolling && (
        <div className="space-y-1.5">
          <p
            className="text-xs text-muted-foreground font-mono"
            data-testid="sync-progress-line"
          >
            {progressLine}
          </p>
          <Progress
            value={progressPercent}
            aria-label={`Sync progress — ${progress.currentStep || 'starting'}`}
            aria-valuenow={progressPercent}
            aria-valuemin={0}
            aria-valuemax={100}
          />
        </div>
      )}

      {progress.status === 'failed' && progress.error && (
        <ErrorEnvelopeRenderer envelope={progress.error} mode="inline" />
      )}

      {mutationError && (
        <ErrorEnvelopeRenderer envelope={mutationError} mode="inline" />
      )}
    </div>
  );
}

/**
 * formatFilterSummary — renders a compact human summary of a mirror
 * filter JSON for display beside the Sync now button. Non-throwing —
 * malformed JSON returns the empty string so the button still renders.
 *
 * Protocol-specific rendering:
 *   - deb:  "{suites} · {components} · {arches}"
 *   - rpm:  "{names}"
 *   - pypi: "{names}" or "{names} · {globs}"
 *   - helm: same as pypi
 */
export function formatFilterSummary(
  filterJSON: string,
  protocol: string,
): string | undefined {
  if (!filterJSON) return undefined;
  let obj: Record<string, unknown>;
  try {
    obj = JSON.parse(filterJSON);
  } catch {
    return undefined;
  }
  const parts: string[] = [];
  if (protocol === 'deb') {
    const suites = (obj.Suites as string[] | undefined) ?? [];
    const comps = (obj.Components as string[] | undefined) ?? [];
    const arches = (obj.Arches as string[] | undefined) ?? [];
    if (suites.length) parts.push(suites.join(', '));
    if (comps.length) parts.push(comps.join(', '));
    if (arches.length) parts.push(arches.join(', '));
  } else {
    const names = (obj.Names as string[] | undefined) ?? [];
    const globs = (obj.Globs as string[] | undefined) ?? [];
    if (names.length) parts.push(names.join(', '));
    if (globs.length) parts.push(`globs: ${globs.join(', ')}`);
  }
  return parts.length ? parts.join(' · ') : undefined;
}
