/**
 * SyncHistoryDialog — F-06.8. Lists recent sync jobs for a mirror repo,
 * backed by GET /projects/{name}/repos/{type}/{repo}/sync-jobs. Opened
 * from the "View history" button next to SyncNowButton. Read-only view:
 * no retry/cancel affordances in this revision (both require backend
 * surface we don't have yet).
 *
 * Shape is deliberately compact — one row per job with status badge,
 * file/byte counts, duration, and last-error tooltip for failed jobs.
 */

import { useMemo } from 'react';
import { Loader2 } from 'lucide-react';
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
  DialogFooter,
} from '@/components/ui/dialog';
import { Button } from '@/components/ui/button';
import {
  StatusBadge,
  type StatusVariant,
} from '@/components/common/StatusBadge';
import { useSyncJobsList } from '@/api/queries';
import { formatBytes, formatDate } from '@/lib/format';
import type { JobDetail } from '@/api/types';

// statusToVariant maps sync_jobs.status onto the 6-value StatusBadge
// palette. Unknown values fall back to `neutral` rather than hiding the
// row so operators still see the wire value in the tooltip. `running`
// uses `maintenance` (active-work icon) rather than `warning` —
// warning's AlertTriangle would falsely signal that a running sync is
// broken (Codex review note on F-06.8).
function statusToVariant(s: string): StatusVariant {
  switch (s) {
    case 'done':
      return 'healthy';
    case 'failed':
      return 'failure';
    case 'running':
      return 'maintenance';
    case 'pending':
      return 'disabled';
    default:
      return 'neutral';
  }
}

export interface SyncHistoryDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  projectName: string;
  repoType: string;
  repoName: string;
}

// driftPurgedCount parses the per-job summary blob and returns the
// drift_purged count if present and > 0. Returns null when the key is
// absent (non-mirror sync, drift_purge=false, or upstream-empty guard
// tripped — D-21 absence rule). Malformed JSON also returns null —
// the dialog still renders the row, just without the drift line.
//
// UIBACK-01 (v1.7): the SyncHistoryDialog surfaces this count as a
// dedicated per-job line so operators can see drift activity at a
// glance without cross-referencing audit events.
function driftPurgedCount(summary: string | undefined): number | null {
  if (!summary) return null;
  try {
    const parsed = JSON.parse(summary) as Record<string, unknown>;
    const v = parsed.drift_purged;
    if (typeof v === 'number' && Number.isFinite(v) && v > 0) {
      return Math.trunc(v);
    }
  } catch {
    // Fall through.
  }
  return null;
}

// durationLabel returns "12s" / "3m 42s" / "—" for pending/unknown.
function durationLabel(job: JobDetail): string {
  // sync_jobs has created_at + updated_at. For terminal states the
  // diff is the total duration; for running jobs we just show
  // "running…" (the live progress bar in SyncNowButton owns the
  // second-resolution update).
  if (job.status === 'pending' || job.status === 'running') {
    return '—';
  }
  const t0 = Date.parse(job.created_at);
  const t1 = Date.parse(job.updated_at);
  if (!Number.isFinite(t0) || !Number.isFinite(t1) || t1 < t0) {
    return '—';
  }
  const seconds = Math.max(0, Math.round((t1 - t0) / 1000));
  if (seconds < 60) return `${seconds}s`;
  const m = Math.floor(seconds / 60);
  const s = seconds % 60;
  return s === 0 ? `${m}m` : `${m}m ${s}s`;
}

export function SyncHistoryDialog({
  open,
  onOpenChange,
  projectName,
  repoType,
  repoName,
}: SyncHistoryDialogProps) {
  const { data, isLoading, isError, error } = useSyncJobsList(
    projectName,
    repoType,
    repoName,
    { enabled: open },
  );

  const jobs = useMemo<JobDetail[]>(() => data?.items ?? [], [data?.items]);

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-3xl">
        <DialogHeader>
          <DialogTitle>Sync history</DialogTitle>
          <DialogDescription>
            Recent sync jobs for{' '}
            <code className="rounded bg-muted px-1 text-xs">
              {projectName}/{repoType}/{repoName}
            </code>
            . Newest first; capped at 25 rows.
          </DialogDescription>
        </DialogHeader>

        <div className="max-h-[60vh] overflow-y-auto">
          {isLoading && (
            <div className="flex items-center justify-center py-8 text-sm text-muted-foreground">
              <Loader2 className="mr-2 size-4 animate-spin" />
              Loading sync jobs…
            </div>
          )}
          {isError && (
            <div className="py-4 text-sm text-destructive">
              Failed to load sync history: {String(error)}
            </div>
          )}
          {!isLoading && !isError && jobs.length === 0 && (
            <div className="py-8 text-center text-sm text-muted-foreground">
              No sync jobs yet. Click Sync now to trigger the first one.
            </div>
          )}
          {!isLoading && !isError && jobs.length > 0 && (
            <table className="w-full border-collapse text-xs">
              <thead>
                <tr className="border-b text-left text-muted-foreground">
                  <th className="px-2 py-1.5 font-semibold">Status</th>
                  <th className="px-2 py-1.5 font-semibold">Started</th>
                  <th className="px-2 py-1.5 font-semibold">Duration</th>
                  <th className="px-2 py-1.5 text-right font-semibold">Files</th>
                  <th className="px-2 py-1.5 text-right font-semibold">Size</th>
                  <th className="px-2 py-1.5 font-semibold">Last step</th>
                </tr>
              </thead>
              <tbody>
                {jobs.map((job) => {
                  const driftPurged = driftPurgedCount(job.summary);
                  return (
                    <tr
                      key={job.id}
                      className="border-b last:border-b-0 align-top"
                      title={
                        job.last_error
                          ? `Last error: ${job.last_error}`
                          : undefined
                      }
                    >
                      <td className="px-2 py-1.5">
                        <StatusBadge
                          status={statusToVariant(job.status)}
                          label={
                            job.attempts > 1
                              ? `${job.status} · attempt ${job.attempts}`
                              : job.status
                          }
                          size="sm"
                        />
                      </td>
                      <td className="px-2 py-1.5 text-muted-foreground">
                        {formatDate(job.created_at)}
                      </td>
                      <td className="px-2 py-1.5 text-muted-foreground tabular-nums">
                        {durationLabel(job)}
                      </td>
                      <td className="px-2 py-1.5 text-right tabular-nums">
                        {job.files_synced.toLocaleString()}
                      </td>
                      <td className="px-2 py-1.5 text-right tabular-nums">
                        {job.total_bytes > 0
                          ? formatBytes(job.total_bytes)
                          : '—'}
                      </td>
                      <td className="px-2 py-1.5 text-muted-foreground">
                        <div>{job.current_step || '—'}</div>
                        {driftPurged !== null && (
                          <div
                            className="mt-0.5 text-xs text-amber-600 dark:text-amber-400 tabular-nums"
                            data-testid="sync-history-drift-purged"
                          >
                            Drift purged: {driftPurged.toLocaleString()}
                          </div>
                        )}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          )}
        </div>

        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            Close
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
