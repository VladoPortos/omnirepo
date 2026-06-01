/**
 * Scan results summary panel.
 * Shows severity bar chart, CVE table, rescan + SBOM download actions.
 */

import { useState, useMemo } from 'react';
import {
  RefreshCw,
  Download,
  ShieldAlert,
  ShieldCheck,
  ExternalLink,
  ChevronDown,
  ChevronRight,
} from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Skeleton } from '@/components/ui/skeleton';
import { SeverityBadge } from './SeverityBadge';
import { InlineSearch } from './InlineSearch';
import { DataTable, type ColumnDef, type SortState } from './DataTable';
import type { Scan, Vulnerability, Severity } from '@/api/types';

const SEVERITY_ORDER: Severity[] = ['CRITICAL', 'HIGH', 'MEDIUM', 'LOW', 'UNKNOWN'];

const SEVERITY_COLORS: Record<string, string> = {
  CRITICAL: 'bg-destructive',
  HIGH: 'bg-orange-500',
  MEDIUM: 'bg-amber-500',
  LOW: 'bg-teal-500',
  UNKNOWN: 'bg-muted-foreground',
};

interface SeverityCounts {
  CRITICAL: number;
  HIGH: number;
  MEDIUM: number;
  LOW: number;
  UNKNOWN: number;
}

function parseSeveritySummary(json: string): SeverityCounts {
  const defaults: SeverityCounts = { CRITICAL: 0, HIGH: 0, MEDIUM: 0, LOW: 0, UNKNOWN: 0 };
  try {
    const parsed = JSON.parse(json);
    return { ...defaults, ...parsed };
  } catch {
    return defaults;
  }
}

interface ScanSummaryProps {
  scan: Scan | null;
  vulnerabilities: Vulnerability[];
  loading?: boolean;
  onRescan?: () => void;
  onDownloadSBOM?: (format: 'cyclonedx' | 'spdx') => void;
  rescanLoading?: boolean;
}

export function ScanSummary({
  scan,
  vulnerabilities,
  loading = false,
  onRescan,
  onDownloadSBOM,
  rescanLoading = false,
}: ScanSummaryProps) {
  const [filter, setFilter] = useState('');
  const [expandedCve, setExpandedCve] = useState<string | null>(null);
  const [sort, setSort] = useState<SortState>({ column: 'severity', direction: 'asc' });

  const counts = useMemo(() => {
    if (!scan?.severity_summary_json) return null;
    return parseSeveritySummary(scan.severity_summary_json);
  }, [scan]);

  const total = useMemo(() => {
    if (!counts) return 0;
    return Object.values(counts).reduce((a, b) => a + b, 0);
  }, [counts]);

  const filteredVulns = useMemo(() => {
    if (!filter) return vulnerabilities;
    const q = filter.toLowerCase();
    return vulnerabilities.filter(
      (v) =>
        v.cve_id.toLowerCase().includes(q) ||
        v.package_name.toLowerCase().includes(q) ||
        v.title.toLowerCase().includes(q),
    );
  }, [vulnerabilities, filter]);

  if (loading) {
    return (
      <div className="space-y-4">
        <Skeleton className="h-6 w-48" />
        <Skeleton className="h-8 w-full" />
        <Skeleton className="h-64 w-full" />
      </div>
    );
  }

  if (!scan) {
    return (
      <div className="flex flex-col items-center justify-center gap-4 py-12 text-center">
        <ShieldAlert className="size-12 text-muted-foreground" />
        <div>
          <h3 className="text-lg font-semibold">Not Scanned Yet</h3>
          <p className="text-sm text-muted-foreground">
            This artifact has not been scanned for vulnerabilities.
          </p>
        </div>
        {onRescan && (
          <Button onClick={onRescan} disabled={rescanLoading}>
            <RefreshCw className={`mr-1.5 size-4 ${rescanLoading ? 'animate-spin' : ''}`} />
            Scan Now
          </Button>
        )}
      </div>
    );
  }

  if (scan.status === 'pending' || scan.status === 'running') {
    return (
      <div className="flex flex-col items-center justify-center gap-4 py-12 text-center">
        <RefreshCw className="size-12 animate-spin text-muted-foreground" />
        <div>
          <h3 className="text-lg font-semibold">Scan In Progress</h3>
          <p className="text-sm text-muted-foreground">
            Vulnerability scan is currently {scan.status}. Results will appear here when complete.
          </p>
        </div>
      </div>
    );
  }

  if (scan.status === 'failed') {
    return (
      <div className="flex flex-col items-center justify-center gap-4 py-12 text-center">
        <ShieldAlert className="size-12 text-destructive" />
        <div>
          <h3 className="text-lg font-semibold">Scan Failed</h3>
          <p className="text-sm text-muted-foreground">
            {scan.last_error || 'An error occurred during scanning.'}
          </p>
        </div>
        {onRescan && (
          <Button onClick={onRescan} disabled={rescanLoading}>
            <RefreshCw className={`mr-1.5 size-4 ${rescanLoading ? 'animate-spin' : ''}`} />
            Retry Scan
          </Button>
        )}
      </div>
    );
  }

  const columns: ColumnDef<Vulnerability>[] = [
    {
      id: 'expand',
      name: '',
      className: 'w-8',
      render: (row) => (
        <button
          onClick={() => setExpandedCve(expandedCve === row.cve_id ? null : row.cve_id)}
          className="text-muted-foreground hover:text-foreground"
        >
          {expandedCve === row.cve_id ? (
            <ChevronDown className="size-4" />
          ) : (
            <ChevronRight className="size-4" />
          )}
        </button>
      ),
    },
    {
      id: 'cve_id',
      name: 'CVE ID',
      sortable: true,
      render: (row) => (
        <a
          href={`https://nvd.nist.gov/vuln/detail/${row.cve_id}`}
          target="_blank"
          rel="noopener noreferrer"
          className="inline-flex items-center gap-1 font-mono text-xs text-primary hover:underline"
        >
          {row.cve_id}
          <ExternalLink className="size-3" />
        </a>
      ),
    },
    {
      id: 'severity',
      name: 'Severity',
      sortable: true,
      render: (row) => <SeverityBadge severity={row.severity} />,
    },
    {
      id: 'package_name',
      name: 'Package',
      sortable: true,
      accessor: (row) => row.package_name,
    },
    {
      id: 'package_version',
      name: 'Installed',
      accessor: (row) => row.package_version,
      className: 'font-mono text-xs',
    },
    {
      id: 'fixed_version',
      name: 'Fixed',
      render: (row) =>
        row.fixed_version ? (
          <span className="font-mono text-xs">{row.fixed_version}</span>
        ) : (
          <span className="text-xs text-muted-foreground">No fix</span>
        ),
    },
  ];

  return (
    <div className="space-y-6">
      {/* Header stats */}
      <div className="flex flex-wrap items-center justify-between gap-4">
        <div className="space-y-1">
          <div className="flex items-center gap-2">
            {total === 0 ? (
              <ShieldCheck className="size-5 text-teal-500" />
            ) : (
              <ShieldAlert className="size-5 text-amber-500" />
            )}
            <h3 className="text-lg font-semibold">
              {total === 0 ? 'No Vulnerabilities Found' : `${total} Vulnerabilities`}
            </h3>
          </div>
          <p className="text-xs text-muted-foreground">
            Last scanned: {scan.finished_at ? new Date(scan.finished_at).toLocaleString() : 'N/A'}
            {scan.trivy_db_version && ` | Trivy DB: ${scan.trivy_db_version}`}
          </p>
        </div>
        <div className="flex gap-2">
          {onRescan && (
            <Button variant="outline" size="sm" onClick={onRescan} disabled={rescanLoading}>
              <RefreshCw className={`mr-1.5 size-4 ${rescanLoading ? 'animate-spin' : ''}`} />
              Rescan
            </Button>
          )}
          {onDownloadSBOM && (
            <div className="flex gap-1">
              <Button variant="outline" size="sm" onClick={() => onDownloadSBOM('cyclonedx')}>
                <Download className="mr-1.5 size-4" />
                CycloneDX
              </Button>
              <Button variant="outline" size="sm" onClick={() => onDownloadSBOM('spdx')}>
                <Download className="mr-1.5 size-4" />
                SPDX
              </Button>
            </div>
          )}
        </div>
      </div>

      {/* Severity bar chart */}
      {counts && total > 0 && (
        <div className="space-y-2">
          <div className="flex h-6 w-full overflow-hidden rounded-md">
            {SEVERITY_ORDER.map((sev) => {
              const count = counts[sev];
              if (count === 0) return null;
              const pct = (count / total) * 100;
              return (
                <div
                  key={sev}
                  className={`${SEVERITY_COLORS[sev]} flex items-center justify-center text-xs font-medium text-white`}
                  style={{ width: `${pct}%`, minWidth: count > 0 ? '24px' : undefined }}
                  title={`${sev}: ${count}`}
                >
                  {pct > 8 ? count : ''}
                </div>
              );
            })}
          </div>
          <div className="flex flex-wrap gap-3 text-xs">
            {SEVERITY_ORDER.map((sev) => (
              <div key={sev} className="flex items-center gap-1.5">
                <div className={`size-2.5 rounded-full ${SEVERITY_COLORS[sev]}`} />
                <span className="text-muted-foreground">
                  {sev.charAt(0) + sev.slice(1).toLowerCase()}: {counts[sev]}
                </span>
              </div>
            ))}
          </div>
        </div>
      )}

      {/* CVE table */}
      {total > 0 && (
        <div className="space-y-3">
          <InlineSearch
            value={filter}
            onChange={setFilter}
            placeholder="Filter by CVE, package, or title..."
            className="max-w-sm"
          />
          <DataTable
            columns={columns}
            data={filteredVulns}
            sort={sort}
            onSort={(col, dir) => setSort({ column: col, direction: dir })}
            emptyMessage="No vulnerabilities match your filter."
          />
          {/* Expanded CVE detail */}
          {expandedCve && (
            <div className="rounded-md border bg-muted/30 p-4">
              {(() => {
                const vuln = vulnerabilities.find((v) => v.cve_id === expandedCve);
                if (!vuln) return null;
                return (
                  <div className="space-y-2">
                    <h4 className="font-semibold">{vuln.title || vuln.cve_id}</h4>
                    <p className="text-sm text-muted-foreground">
                      {vuln.description || 'No description available.'}
                    </p>
                  </div>
                );
              })()}
            </div>
          )}
        </div>
      )}
    </div>
  );
}
