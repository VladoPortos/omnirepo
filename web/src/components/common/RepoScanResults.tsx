/**
 * List of scans for a single repo, shown inside the repo page's
 * "Scan Results" tab. Uses useRepoScans + the repo-level /scans endpoint.
 *
 * Per-row rendering:
 *  - artifact_id shortened to a fingerprint
 *  - status badge (done / scanning / failed)
 *  - severity counts (critical..low) as compact badges
 *  - finished_at timestamp
 *
 * Empty / loading / failure states are handled inline so the parent tab
 * can just render <RepoScanResults .../>.
 */

import { useMemo, useState } from 'react';
import {
  RefreshCw,
  ShieldCheck,
  ShieldAlert,
  ShieldX,
  Clock,
  Loader2,
  ChevronDown,
  ChevronRight,
  Trash2,
} from 'lucide-react';
import { toast } from 'sonner';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Skeleton } from '@/components/ui/skeleton';
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
  DialogFooter,
} from '@/components/ui/dialog';
import { useRepoScans, useRescanRepo, usePruneScans, useMe } from '@/api/queries';
import { formatDate } from '@/lib/format';
import { ApiError } from '@/api/client';
import type { Scan } from '@/api/types';

interface RepoScanResultsProps {
  projectName: string;
  repoType: string;
  repoName: string;
}

interface SeverityCounts {
  critical: number;
  high: number;
  medium: number;
  low: number;
  unknown: number;
}

function parseSummary(json: string): SeverityCounts {
  const zero: SeverityCounts = { critical: 0, high: 0, medium: 0, low: 0, unknown: 0 };
  if (!json) return zero;
  try {
    return { ...zero, ...JSON.parse(json) };
  } catch {
    return zero;
  }
}

function shortDigest(s: string): string {
  // sha256:abcdef01234567... -> sha256:abcdef01
  if (!s.startsWith('sha256:')) return s;
  return s.slice(0, 'sha256:'.length + 12);
}

// summarizeScanError condenses a raw scan.last_error (which may be several
// lines of Trivy stderr plus Go wrapping) into a single short phrase
// suitable for an inline badge. Known failure modes get bespoke copy so
// the user knows what to do next; everything else falls back to the first
// non-empty line of the underlying error.
function summarizeScanError(raw: string): string {
  if (!raw) return 'Scan failed.';
  if (raw.includes('--skip-db-update cannot be specified on the first run')) {
    return 'Trivy database not installed — ask an administrator to upload it at /admin/trivy.';
  }
  if (raw.includes('artifact file missing on disk')) {
    return 'The uploaded file is missing from disk (data volume corruption or manual removal).';
  }
  if (raw.includes('trivy: trivy exec failed')) {
    return 'Trivy scan crashed. See scan row details for the full log.';
  }
  // Generic: take the first non-empty line and trim.
  for (const line of raw.split('\n')) {
    const trimmed = line.trim();
    if (trimmed) {
      return trimmed.length > 200 ? trimmed.slice(0, 200) + '…' : trimmed;
    }
  }
  return 'Scan failed.';
}

function StatusBadge({ scan }: { scan: Scan }) {
  if (scan.status === 'done') {
    const counts = parseSummary(scan.severity_summary_json);
    const total = counts.critical + counts.high + counts.medium + counts.low;
    if (total === 0) {
      return (
        <Badge variant="outline" className="bg-teal-500/10 text-teal-600 border-teal-500/20 dark:text-teal-400">
          <ShieldCheck className="mr-1 size-3" />
          Clean
        </Badge>
      );
    }
    return (
      <Badge variant="outline" className="bg-amber-500/10 text-amber-600 border-amber-500/20 dark:text-amber-400">
        <ShieldAlert className="mr-1 size-3" />
        Done
      </Badge>
    );
  }
  if (scan.status === 'pending' || scan.status === 'running') {
    return (
      <Badge variant="outline">
        <RefreshCw className="mr-1 size-3 animate-spin" />
        {scan.status === 'running' ? 'Running' : 'Queued'}
      </Badge>
    );
  }
  // Failed — expose the reason on hover so operators don't have to go
  // digging in the scan detail panel.
  return (
    <Badge
      variant="outline"
      className="bg-destructive/10 text-destructive border-destructive/20"
      title={scan.last_error || 'Scan failed.'}
    >
      <ShieldX className="mr-1 size-3" />
      Failed
    </Badge>
  );
}

function CountCell({ label, value, className }: { label: string; value: number; className: string }) {
  if (value === 0) {
    return <span className="text-muted-foreground">—</span>;
  }
  return (
    <span className={className} title={`${label}: ${value}`}>
      {value}
    </span>
  );
}

// groupKeyFor returns the YYYY-MM-DD day a scan should live under — we
// prefer finished_at (terminal rows) but fall back to started_at →
// created_at so running rows still land in a sensible bucket.
function groupKeyFor(scan: Scan): string {
  const ts = scan.finished_at || scan.started_at || scan.created_at || '';
  if (!ts) return 'unknown';
  // The scan API emits RFC3339; pick the date portion (UTC).
  return ts.slice(0, 10);
}

// humanDayLabel turns YYYY-MM-DD into "Today", "Yesterday", or the long
// date — scannable at a glance while still showing the exact day for
// older entries.
function humanDayLabel(key: string): string {
  if (key === 'unknown') return 'No timestamp';
  const today = new Date();
  const todayKey = today.toISOString().slice(0, 10);
  if (key === todayKey) return `Today (${key})`;
  const yesterday = new Date(today);
  yesterday.setDate(today.getDate() - 1);
  const yKey = yesterday.toISOString().slice(0, 10);
  if (key === yKey) return `Yesterday (${key})`;
  return key;
}

const PAGE_SIZE = 100;

export function RepoScanResults({ projectName, repoType, repoName }: RepoScanResultsProps) {
  // Paginated list: bump `limit` as the user clicks "Load more".
  const [limit, setLimit] = useState(PAGE_SIZE);
  const { data: scans, isLoading, isError } = useRepoScans(projectName, repoType, repoName, {
    limit,
  });
  const { data: me } = useMe();
  const rescan = useRescanRepo(projectName, repoType, repoName);
  const prune = usePruneScans(projectName, repoType, repoName);
  const [pruneOpen, setPruneOpen] = useState(false);
  // Track which date groups the user has opened. Closed by default so a
  // repo with years of scan history doesn't render as a wall of rows.
  const [openGroups, setOpenGroups] = useState<Set<string>>(new Set());

  const rows = useMemo(() => scans ?? [], [scans]);
  const hasMore = rows.length >= limit;
  // Git repos don't have scannable artifacts. Rendering the button there
  // would 400 on click — hide it instead. Everyone else gets the button
  // (server enforces the real permission check).
  const canRescan = repoType !== 'git' && !!me;

  // Group scans by day key; preserves per-group ordering (newest first
  // because the API already sorts by id DESC, which correlates with
  // timestamp).
  const groups = useMemo(() => {
    const map = new Map<string, Scan[]>();
    for (const scan of rows) {
      const key = groupKeyFor(scan);
      const bucket = map.get(key) ?? [];
      bucket.push(scan);
      map.set(key, bucket);
    }
    // Order groups by key DESC so "today" is first. Guard the "unknown"
    // bucket to the bottom so it doesn't claim pole position.
    return Array.from(map.entries()).sort(([a], [b]) => {
      if (a === 'unknown') return 1;
      if (b === 'unknown') return -1;
      return b.localeCompare(a);
    });
  }, [rows]);

  const handleRescanAll = async () => {
    try {
      const res = await rescan.mutateAsync();
      if (res.enqueued === 0) {
        toast.info('No artifacts to scan in this repo.');
        return;
      }
      toast.success(
        `Queued ${res.enqueued} scan${res.enqueued === 1 ? '' : 's'}. Results will appear shortly.`,
      );
    } catch (err) {
      if (err instanceof ApiError && err.status === 412) {
        toast.error(
          'Trivy database is not installed. An administrator must install it at /admin/trivy.',
        );
        return;
      }
      if (err instanceof ApiError) {
        toast.error(err.detail || err.message || 'Rescan failed.');
        return;
      }
      toast.error('Rescan failed.');
    }
  };

  const handlePrune = async () => {
    try {
      const res = await prune.mutateAsync();
      if (res.deleted === 0) {
        toast.info('Nothing to prune — history is already one-per-artifact.');
      } else {
        toast.success(
          `Removed ${res.deleted} older scan${res.deleted === 1 ? '' : 's'} (kept ${res.kept}).`,
        );
      }
    } catch (err) {
      if (err instanceof ApiError) {
        toast.error(err.detail || err.message || 'Prune failed.');
      } else {
        toast.error('Prune failed.');
      }
    } finally {
      setPruneOpen(false);
    }
  };

  const toggleGroup = (key: string) => {
    setOpenGroups((prev) => {
      const next = new Set(prev);
      if (next.has(key)) {
        next.delete(key);
      } else {
        next.add(key);
      }
      return next;
    });
  };

  const header = canRescan ? (
    <div className="flex flex-wrap items-center justify-between gap-3 pb-3">
      <div>
        <h3 className="text-sm font-semibold">Scan Results</h3>
        <p className="text-xs text-muted-foreground">
          Run a fresh scan against every artifact, or prune the history to one
          row per artifact.
        </p>
      </div>
      <div className="flex items-center gap-2">
        <Button
          variant="outline"
          size="sm"
          onClick={() => setPruneOpen(true)}
          disabled={prune.isPending || rows.length === 0}
          title="Keep only the newest scan per artifact, delete older ones"
        >
          {prune.isPending ? (
            <Loader2 className="mr-1.5 size-4 animate-spin" />
          ) : (
            <Trash2 className="mr-1.5 size-4" />
          )}
          {prune.isPending ? 'Pruning…' : 'Prune old scans'}
        </Button>
        <Button
          variant="outline"
          size="sm"
          onClick={handleRescanAll}
          disabled={rescan.isPending}
        >
          {rescan.isPending ? (
            <Loader2 className="mr-1.5 size-4 animate-spin" />
          ) : (
            <RefreshCw className="mr-1.5 size-4" />
          )}
          {rescan.isPending ? 'Queuing…' : 'Rescan all'}
        </Button>
      </div>
    </div>
  ) : null;

  const pruneDialog = (
    <Dialog open={pruneOpen} onOpenChange={setPruneOpen}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Prune old scan history</DialogTitle>
          <DialogDescription>
            Delete every finished scan except the newest one per artifact in
            this repository. In-flight (pending or running) scans are left
            alone. This cannot be undone.
          </DialogDescription>
        </DialogHeader>
        <DialogFooter>
          <Button variant="outline" onClick={() => setPruneOpen(false)}>
            Cancel
          </Button>
          <Button variant="destructive" onClick={handlePrune} disabled={prune.isPending}>
            {prune.isPending ? (
              <Loader2 className="mr-1.5 size-4 animate-spin" />
            ) : (
              <Trash2 className="mr-1.5 size-4" />
            )}
            Delete older scans
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );

  if (isLoading) {
    return (
      <div className="space-y-3 py-4">
        {header}
        <Skeleton className="h-8 w-full" />
        <Skeleton className="h-8 w-full" />
        <Skeleton className="h-8 w-full" />
        {pruneDialog}
      </div>
    );
  }

  if (isError) {
    return (
      <div className="py-4">
        {header}
        <div className="flex items-center justify-center gap-2 py-12 text-sm text-destructive">
          <ShieldX className="size-4" />
          Failed to load scans.
        </div>
        {pruneDialog}
      </div>
    );
  }

  if (rows.length === 0) {
    return (
      <div className="py-4">
        {header}
        <div className="flex flex-col items-center justify-center gap-3 py-12 text-center">
          <Clock className="size-10 text-muted-foreground" />
          <div>
            <h3 className="text-sm font-semibold">No scans yet</h3>
            <p className="text-xs text-muted-foreground">
              Scans are enqueued automatically when an artifact is uploaded. Use
              the button above to scan existing artifacts now.
            </p>
          </div>
        </div>
        {pruneDialog}
      </div>
    );
  }

  return (
    <div className="py-2">
      {header}
      <div className="space-y-2">
        {groups.map(([key, groupRows]) => {
          const isOpen = openGroups.has(key);
          return (
            <div key={key} className="rounded-md border">
              <button
                type="button"
                onClick={() => toggleGroup(key)}
                className="flex w-full items-center justify-between gap-3 px-3 py-2 text-left hover:bg-muted/30"
                aria-expanded={isOpen}
              >
                <div className="flex items-center gap-2">
                  {isOpen ? (
                    <ChevronDown className="size-4 text-muted-foreground" />
                  ) : (
                    <ChevronRight className="size-4 text-muted-foreground" />
                  )}
                  <span className="text-sm font-medium">{humanDayLabel(key)}</span>
                </div>
                <span className="text-xs text-muted-foreground">
                  {groupRows.length} scan{groupRows.length === 1 ? '' : 's'}
                </span>
              </button>
              {isOpen && <ScanRowsTable rows={groupRows} />}
            </div>
          );
        })}
        {hasMore && (
          <div className="flex justify-center pt-2">
            <Button
              variant="outline"
              size="sm"
              onClick={() => setLimit((n) => n + PAGE_SIZE)}
            >
              Load more
            </Button>
          </div>
        )}
      </div>
      {pruneDialog}
    </div>
  );
}

// ScanRowsTable renders the per-row details for one date group. Pulled
// out of the main component so the date collapsibles can lazy-render
// (table only mounts when the group is expanded).
function ScanRowsTable({ rows }: { rows: Scan[] }) {
  return (
    <div className="overflow-x-auto border-t">
      <table className="w-full text-sm">
          <thead className="border-b">
            <tr className="text-left text-xs font-medium text-muted-foreground">
              <th className="px-3 py-2">Artifact</th>
              <th className="px-3 py-2">Status</th>
              <th className="px-3 py-2 text-right">Crit</th>
              <th className="px-3 py-2 text-right">High</th>
              <th className="px-3 py-2 text-right">Med</th>
              <th className="px-3 py-2 text-right">Low</th>
              <th className="px-3 py-2">Finished</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((scan) => {
              const counts = parseSummary(scan.severity_summary_json);
              const failureSummary =
                scan.status === 'failed' ? summarizeScanError(scan.last_error) : null;
              return (
                <tr key={scan.id} className="border-b last:border-0 hover:bg-muted/30">
                  <td className="px-3 py-2">
                    <div className="flex flex-col">
                      <span className="font-mono text-xs" title={scan.artifact_id}>
                        {shortDigest(scan.artifact_id)}
                      </span>
                      <span className="text-xs text-muted-foreground">{scan.artifact_kind}</span>
                    </div>
                  </td>
                  <td className="px-3 py-2">
                    <StatusBadge scan={scan} />
                    {failureSummary && (
                      <p className="mt-1 max-w-xl text-xs text-destructive">{failureSummary}</p>
                    )}
                  </td>
                  <td className="px-3 py-2 text-right">
                    <CountCell label="Critical" value={counts.critical} className="font-medium text-destructive" />
                  </td>
                  <td className="px-3 py-2 text-right">
                    <CountCell
                      label="High"
                      value={counts.high}
                      className="font-medium text-orange-600 dark:text-orange-400"
                    />
                  </td>
                  <td className="px-3 py-2 text-right">
                    <CountCell
                      label="Medium"
                      value={counts.medium}
                      className="font-medium text-amber-600 dark:text-amber-400"
                    />
                  </td>
                  <td className="px-3 py-2 text-right">
                    <CountCell
                      label="Low"
                      value={counts.low}
                      className="font-medium text-teal-600 dark:text-teal-400"
                    />
                  </td>
                  <td className="px-3 py-2 text-xs text-muted-foreground">
                    {scan.finished_at ? formatDate(scan.finished_at) : '—'}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
  );
}
