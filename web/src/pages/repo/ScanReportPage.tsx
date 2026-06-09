/**
 * Standalone per-artifact scan report page.
 *
 * Route: /projects/:name/:type/:repo/scans/:id
 *
 * Shows:
 *   - artifact identity + scan metadata header
 *   - group-by-package summary (top CVE counts per package)
 *   - severity filter chips + text search
 *   - vulnerability table with external NVD links
 *   - SBOM download
 *
 * Renders even when the URL's :type doesn't match the scan's
 * artifact_kind — we read the authoritative repo_id + artifact_kind
 * from the scan row, so the breadcrumb path is cosmetic.
 */

import { Fragment, useMemo, useState } from 'react';
import { Link, useParams } from 'react-router-dom';
import {
  ArrowLeft,
  Download,
  ExternalLink,
  PackageOpen,
  ShieldAlert,
} from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Badge } from '@/components/ui/badge';
import { ToggleGroup, ToggleGroupItem } from '@/components/ui/toggle-group';
import { SeverityBadge } from '@/components/common/SeverityBadge';
import { InlineSearch } from '@/components/common/InlineSearch';
import { EmptyState } from '@/components/common/EmptyState';
import { SkeletonCard } from '@/components/common/SkeletonCard';
import { useScan, useScanVulnerabilities, sbomDownloadURL } from '@/api/queries';
import { formatDate } from '@/lib/format';
import type { Vulnerability } from '@/api/types';

const SEVERITY_ORDER = ['CRITICAL', 'HIGH', 'MEDIUM', 'LOW', 'UNKNOWN'] as const;
type SeverityUpper = (typeof SEVERITY_ORDER)[number];

interface SeverityCounts {
  critical?: number;
  high?: number;
  medium?: number;
  low?: number;
  unknown?: number;
}

function parseSummary(raw: string): SeverityCounts {
  if (!raw) return {};
  try {
    return JSON.parse(raw) as SeverityCounts;
  } catch {
    return {};
  }
}

function nvdURL(cveID: string): string {
  // NVD is the canonical CVE record. Ghsa / vendor advisories could be
  // layered in later, but NVD works for every CVE ID we might see.
  return `https://nvd.nist.gov/vuln/detail/${encodeURIComponent(cveID)}`;
}

export function ScanReportPage() {
  const { name: projectName, type: repoType, repo: repoName, id: scanIDParam } =
    useParams<{ name: string; type: string; repo: string; id: string }>();
  const scanID = Number(scanIDParam);

  const scanQ = useScan(scanID);
  const vulnsQ = useScanVulnerabilities(scanID);

  const [severityFilter, setSeverityFilter] = useState<SeverityUpper[]>([]);
  const [search, setSearch] = useState('');

  // Stable identity so the memos below don't recompute every render.
  const vulns = useMemo(() => vulnsQ.data ?? [], [vulnsQ.data]);
  const summary = useMemo(
    () => parseSummary(scanQ.data?.severity_summary_json ?? ''),
    [scanQ.data],
  );

  const filtered = useMemo(() => {
    const q = search.toLowerCase().trim();
    return vulns.filter((v) => {
      if (
        severityFilter.length > 0 &&
        !severityFilter.includes(v.severity as SeverityUpper)
      ) {
        return false;
      }
      if (!q) return true;
      return (
        v.cve_id.toLowerCase().includes(q) ||
        v.package_name.toLowerCase().includes(q) ||
        v.title.toLowerCase().includes(q)
      );
    });
  }, [vulns, severityFilter, search]);

  // Group-by-package — derived from unfiltered vulns so the summary
  // always shows the full shape of the repo's exposure, not a filtered
  // subset.
  const byPackage = useMemo(() => {
    const acc = new Map<
      string,
      { total: number; counts: Record<string, number> }
    >();
    for (const v of vulns) {
      const entry = acc.get(v.package_name) ?? { total: 0, counts: {} };
      entry.total += 1;
      const key = v.severity.toLowerCase();
      entry.counts[key] = (entry.counts[key] ?? 0) + 1;
      acc.set(v.package_name, entry);
    }
    return Array.from(acc.entries())
      .map(([name, v]) => ({ name, ...v }))
      .sort((a, b) => b.total - a.total)
      .slice(0, 8);
  }, [vulns]);

  if (scanQ.isLoading) {
    return (
      <div className="space-y-4">
        <SkeletonCard />
      </div>
    );
  }
  if (scanQ.isError || !scanQ.data) {
    return (
      <EmptyState
        icon={ShieldAlert}
        title="Scan not found"
        description="This scan has been pruned or never existed."
      />
    );
  }

  const scan = scanQ.data;
  const totalVulns = vulns.length;
  const repoHref = `/projects/${encodeURIComponent(projectName ?? '')}/${encodeURIComponent(repoType ?? '')}/${encodeURIComponent(repoName ?? '')}`;

  return (
    <div className="space-y-4">
      {/* Header */}
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0 space-y-1">
          <div className="flex items-center gap-2">
            <Button
              variant="ghost"
              size="sm"
              nativeButton={false}
              render={<Link to={repoHref} />}
            >
              <ArrowLeft className="mr-1.5 size-4" />
              Back to repo
            </Button>
          </div>
          <h1 className="truncate text-2xl font-semibold" title={scan.artifact_id}>
            {scan.artifact_id}
          </h1>
          <p className="text-sm text-muted-foreground">
            {scan.artifact_kind} · scanned {formatDate(scan.finished_at || scan.started_at || scan.created_at)}
            {scan.trivy_db_version && (
              <>
                {' · Trivy DB '}
                <code className="text-xs">{scan.trivy_db_version}</code>
              </>
            )}
          </p>
        </div>
        <div className="flex flex-wrap gap-2">
          <Button
            variant="outline"
            size="sm"
            nativeButton={false}
            render={<a href={sbomDownloadURL(scan.id)} />}
          >
            <Download className="mr-1.5 size-4" />
            Download SBOM
          </Button>
        </div>
      </div>

      {/* Severity summary strip */}
      {totalVulns > 0 ? (
        <div className="flex flex-wrap items-center gap-2 rounded-md border bg-muted/30 px-3 py-2">
          <span className="text-sm font-semibold">
            {totalVulns.toLocaleString()} finding{totalVulns === 1 ? '' : 's'}:
          </span>
          {SEVERITY_ORDER.map((sev) => {
            const n = summary[sev.toLowerCase() as keyof SeverityCounts] ?? 0;
            if (n === 0) return null;
            return <SeverityBadge key={sev} severity={sev.toLowerCase()}>{n}</SeverityBadge>;
          })}
        </div>
      ) : vulnsQ.isSuccess ? (
        <div className="rounded-md border border-teal-500/20 bg-teal-500/5 px-3 py-2 text-sm text-teal-700 dark:text-teal-400">
          No vulnerabilities found — last scan reported clean.
        </div>
      ) : null}

      {/* Group-by-package summary */}
      {byPackage.length > 0 && (
        <div className="rounded-md border">
          <div className="flex items-center gap-2 border-b px-3 py-2">
            <PackageOpen className="size-4 text-muted-foreground" />
            <h2 className="text-sm font-semibold">Top affected packages</h2>
          </div>
          <ul className="divide-y">
            {byPackage.map((p) => (
              <li key={p.name} className="flex items-center justify-between gap-3 px-3 py-2">
                <button
                  type="button"
                  className="truncate font-mono text-sm text-primary hover:underline"
                  onClick={() => setSearch(p.name)}
                  title={`Filter to ${p.name}`}
                >
                  {p.name}
                </button>
                <div className="flex shrink-0 items-center gap-1.5">
                  {(['critical', 'high', 'medium', 'low', 'unknown'] as const).map((sev) => {
                    const n = p.counts[sev] ?? 0;
                    if (n === 0) return null;
                    return (
                      <SeverityBadge key={sev} severity={sev}>
                        {n}
                      </SeverityBadge>
                    );
                  })}
                  <Badge variant="outline" className="text-xs tabular-nums">
                    {p.total}
                  </Badge>
                </div>
              </li>
            ))}
          </ul>
        </div>
      )}

      {/* Filters */}
      {totalVulns > 0 && (
        <div className="flex flex-wrap items-center gap-2">
          <ToggleGroup
            multiple
            size="sm"
            value={severityFilter}
            onValueChange={(v) => setSeverityFilter(v as SeverityUpper[])}
          >
            {SEVERITY_ORDER.map((sev) => {
              const n = summary[sev.toLowerCase() as keyof SeverityCounts] ?? 0;
              if (n === 0) return null;
              return (
                <ToggleGroupItem key={sev} value={sev} className="text-xs">
                  {sev.charAt(0) + sev.slice(1).toLowerCase()} ({n})
                </ToggleGroupItem>
              );
            })}
          </ToggleGroup>
          <InlineSearch
            value={search}
            onChange={setSearch}
            placeholder="Search CVE, package, title..."
            className="max-w-sm"
          />
        </div>
      )}

      {/* Vuln table */}
      {totalVulns > 0 && (
        <VulnerabilityTable rows={filtered} totalUnfiltered={totalVulns} />
      )}
    </div>
  );
}

function VulnerabilityTable({
  rows,
  totalUnfiltered,
}: {
  rows: Vulnerability[];
  totalUnfiltered: number;
}) {
  const [expanded, setExpanded] = useState<number | null>(null);

  if (rows.length === 0) {
    return (
      <p className="rounded-md border bg-muted/20 px-3 py-6 text-center text-sm text-muted-foreground">
        No findings match the current filter. {totalUnfiltered.toLocaleString()} total in scan.
      </p>
    );
  }

  return (
    <div className="overflow-x-auto rounded-md border">
      <table className="w-full text-sm">
        <thead className="border-b bg-muted/30 text-left">
          <tr>
            <th className="px-3 py-2 font-semibold">Severity</th>
            <th className="px-3 py-2 font-semibold">CVE</th>
            <th className="px-3 py-2 font-semibold">Package</th>
            <th className="px-3 py-2 font-semibold">Installed</th>
            <th className="px-3 py-2 font-semibold">Fixed in</th>
            <th className="px-3 py-2 font-semibold">Title</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((v) => (
            <Fragment key={v.id}>
              <tr
                className="cursor-pointer border-b last:border-b-0 hover:bg-muted/40"
                onClick={() => setExpanded(expanded === v.id ? null : v.id)}
              >
                <td className="px-3 py-2 align-top">
                  <SeverityBadge severity={v.severity.toLowerCase()} />
                </td>
                <td className="px-3 py-2 align-top">
                  <a
                    className="inline-flex items-center gap-1 font-mono text-xs text-primary hover:underline"
                    href={nvdURL(v.cve_id)}
                    target="_blank"
                    rel="noopener noreferrer"
                    onClick={(e) => e.stopPropagation()}
                  >
                    {v.cve_id}
                    <ExternalLink className="size-3" />
                  </a>
                </td>
                <td className="px-3 py-2 align-top font-mono text-xs">{v.package_name}</td>
                <td className="px-3 py-2 align-top font-mono text-xs text-muted-foreground">
                  {v.package_version || '—'}
                </td>
                <td className="px-3 py-2 align-top font-mono text-xs">
                  {v.fixed_version ? (
                    <span className="text-teal-600 dark:text-teal-400">{v.fixed_version}</span>
                  ) : (
                    <span className="text-muted-foreground">—</span>
                  )}
                </td>
                <td className="px-3 py-2 align-top">
                  <span className="line-clamp-2">{v.title || '(no title)'}</span>
                </td>
              </tr>
              {expanded === v.id && v.description && (
                <tr className="border-b bg-muted/20">
                  <td colSpan={6} className="px-3 py-3 text-sm text-muted-foreground">
                    {v.description}
                  </td>
                </tr>
              )}
            </Fragment>
          ))}
        </tbody>
      </table>
    </div>
  );
}
