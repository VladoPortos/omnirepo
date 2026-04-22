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
 *     concurrency via 409 mirror.sync.in_flight, but we don't
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
import { StatusBadge } from '@/components/common/StatusBadge';
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

  // POLISH-01 (Plan 09-05): success pill state. `pillVisible` drives
  // rendering + auto-dismiss; `pillSnapshot` freezes the terminal progress
  // numbers at the moment `status === 'done'` fires so the pill content
  // stays stable even if the underlying query re-fetches after the job
  // finishes. `progress XOR confirmation` (D-06) is enforced in handleClick
  // which clears the pill on re-click.
  const [pillVisible, setPillVisible] = useState(false);
  const [pillSnapshot, setPillSnapshot] = useState<{
    bytes: number;
    totalBytes: number;
    step: string;
    // Quick task 260420-d03 (D-03 closure): files newly added during the
    // sync. Written once by the backend at sync completion; snapshotting
    // here freezes the value so the pill stays stable during its 8s life
    // even if the underlying query re-fetches.
    filesSynced: number;
  } | null>(null);

  const isPolling = progress.isPolling;
  const disabled = mutation.isPending || isPolling;

  // When the polled job terminates, invalidate content + repo caches
  // so the UI reflects newly-synced artifacts without a manual refresh.
  // POLISH-01: ALSO snapshot the terminal progress numbers and flip the
  // pill on — the separate auto-dismiss effect below arms the 8s timer.
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
      setPillSnapshot({
        bytes: progress.progressBytes,
        totalBytes: progress.totalBytes,
        step: progress.currentStep,
        filesSynced: progress.filesSynced,
      });
      setPillVisible(true);
    }
  }, [
    progress.status,
    jobId,
    projectName,
    repoType,
    repoName,
    qc,
    progress.progressBytes,
    progress.totalBytes,
    progress.currentStep,
    progress.filesSynced,
  ]);

  // POLISH-01: Auto-dismiss the pill 8 seconds after it becomes visible
  // (D-02). Cleanup handles the re-click path (handleClick flips
  // pillVisible false → this effect re-runs → previous timer cleared).
  useEffect(() => {
    if (!pillVisible) return;
    const t = setTimeout(() => setPillVisible(false), 8000);
    return () => clearTimeout(t);
  }, [pillVisible]);

  const handleClick = () => {
    // POLISH-01 (D-06): clear the confirmation pill immediately so the
    // progress block and pill are never on-screen simultaneously
    // (progress XOR confirmation). Timer cleanup happens via the
    // auto-dismiss effect above.
    setPillVisible(false);
    setPillSnapshot(null);
    setMutationError(null);
    // F-06.6 Codex follow-up: drop the current jobId before kicking off
    // a new sync. If the previous job ended with a retry-backoff or
    // terminal-failed envelope, that envelope stays rendered on a stale
    // jobId until the mutation resolves — visually stacking against any
    // mutationError from the new attempt. Setting jobId=null disables
    // useJobProgress (returns idleJobProgress), so the error vanishes
    // until the new job_id lands in onSuccess.
    setJobId(null);
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

  // POLISH-01 + quick task 260420-d03 (D-03 closure): build the pill
  // label from the frozen snapshot. The leading ✓ is provided by
  // StatusBadge's `healthy` variant (CheckCircle2), so we must NOT prepend
  // a ✓ glyph to the label text — that would render two checkmarks.
  //
  // D-03 literal shape: `✓ Sync complete · N files · X MB`. Now that the
  // backend surfaces files_synced at sync completion (migration 025 +
  // SyncJobsRepo.SetFilesSynced), the pill renders the full shape when
  // both bytes and files are known.
  // Fallback ladder:
  //   1. totalBytes > 0 && filesSynced > 0 → `Sync complete · N file(s) · X MB`
  //      (the full D-03 shape; singular "file" at N=1, plural "files" otherwise)
  //   2. totalBytes > 0                    → `Sync complete · X MB`
  //      (e.g. RPM/APT/PyPI where the sync persisted 0 newly-added files —
  //      every package was already present — but bytes were scanned)
  //   3. step present (Helm, total_bytes=0) → `Sync complete · <step>`
  //      (preserves 09-05's Helm fallback; e.g. "chart 5 of 5")
  //   4. otherwise                         → `Sync complete`
  const pillContent = (() => {
    if (!pillSnapshot) return 'Sync complete';
    const { bytes, totalBytes, step, filesSynced } = pillSnapshot;
    if (totalBytes > 0 && filesSynced > 0) {
      const noun = filesSynced === 1 ? 'file' : 'files';
      return `Sync complete · ${filesSynced} ${noun} · ${formatBytes(bytes)}`;
    }
    if (totalBytes > 0) {
      return `Sync complete · ${formatBytes(bytes)}`;
    }
    if (step) return `Sync complete · ${step}`;
    return 'Sync complete';
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

      {pillVisible && (
        <div
          role="status"
          aria-live="polite"
          data-testid="sync-complete-pill"
          className="transition-opacity duration-150 motion-reduce:duration-0"
        >
          <StatusBadge status="healthy" label={pillContent} size="sm" />
        </div>
      )}

      {/*
        F-06.6 follow-up (Codex pass): useJobProgress now emits a
        transient-class `job.retrying` envelope while status stays
        `pending` between retry-backoff attempts. Pre-fix this render
        gate required `status === 'failed'` — it silently swallowed
        every retrying envelope for up to 96 minutes. Show the
        envelope whenever computeJobProgress produced one; the
        envelope carries its own class/code, so the UI renders the
        correct severity.
      */}
      {progress.error && (
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
