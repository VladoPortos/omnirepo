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

import { useMemo } from 'react';
import { RefreshCw, ShieldCheck, ShieldAlert, ShieldX, Clock } from 'lucide-react';
import { Badge } from '@/components/ui/badge';
import { Skeleton } from '@/components/ui/skeleton';
import { useRepoScans } from '@/api/queries';
import { formatDate } from '@/lib/format';
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
  return (
    <Badge variant="outline" className="bg-destructive/10 text-destructive border-destructive/20">
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

export function RepoScanResults({ projectName, repoType, repoName }: RepoScanResultsProps) {
  const { data: scans, isLoading, isError } = useRepoScans(projectName, repoType, repoName);

  const rows = useMemo(() => scans ?? [], [scans]);

  if (isLoading) {
    return (
      <div className="space-y-3 py-4">
        <Skeleton className="h-8 w-full" />
        <Skeleton className="h-8 w-full" />
        <Skeleton className="h-8 w-full" />
      </div>
    );
  }

  if (isError) {
    return (
      <div className="flex items-center justify-center gap-2 py-12 text-sm text-destructive">
        <ShieldX className="size-4" />
        Failed to load scans.
      </div>
    );
  }

  if (rows.length === 0) {
    return (
      <div className="flex flex-col items-center justify-center gap-3 py-12 text-center">
        <Clock className="size-10 text-muted-foreground" />
        <div>
          <h3 className="text-sm font-semibold">No scans yet</h3>
          <p className="text-xs text-muted-foreground">
            Scans are enqueued automatically when an artifact is uploaded to this repository.
          </p>
        </div>
      </div>
    );
  }

  return (
    <div className="py-2">
      <div className="overflow-x-auto">
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
    </div>
  );
}
