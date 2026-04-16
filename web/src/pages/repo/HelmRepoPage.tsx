/**
 * Helm chart repository detail page per D-12.
 * Table grouped by chart name (expandable to see versions).
 * Columns: Chart Name, Version, App Version, Size, Upload Date, Scan Status.
 */

import { useState, useMemo } from 'react';
import { ChevronDown, ChevronRight, Layers } from 'lucide-react';
import { DataTable, type ColumnDef, type SortState } from '@/components/common/DataTable';
import { SeverityBadge } from '@/components/common/SeverityBadge';
import { InlineSearch } from '@/components/common/InlineSearch';
import { Dropzone } from '@/components/common/Dropzone';
import { RepoPageLayout } from './RepoPageLayout';
import { formatBytes, formatDate } from '@/lib/format';
import { api } from '@/api/client';
import type { Repo } from '@/api/types';

interface HelmChartVersion {
  id: number;
  chart_name: string;
  version: string;
  app_version: string;
  size: number;
  scan_severity: string;
  uploaded_at: string;
}

/** Grouped view: one row per chart name. */
interface HelmChartGroup {
  chart_name: string;
  latest_version: string;
  latest_app_version: string;
  version_count: number;
  total_size: number;
  scan_severity: string;
  uploaded_at: string;
  versions: HelmChartVersion[];
}

interface HelmRepoPageProps {
  repo: Repo;
}

export function HelmRepoPage({ repo }: HelmRepoPageProps) {
  const [filter, setFilter] = useState('');
  const [sort, setSort] = useState<SortState>({ column: 'chart_name', direction: 'asc' });
  const [expandedChart, setExpandedChart] = useState<string | null>(null);

  // Charts fetched from API; placeholder empty
  const chartVersions: HelmChartVersion[] = [];

  // Group by chart name
  const groups = useMemo<HelmChartGroup[]>(() => {
    const map = new Map<string, HelmChartVersion[]>();
    for (const cv of chartVersions) {
      const existing = map.get(cv.chart_name) ?? [];
      existing.push(cv);
      map.set(cv.chart_name, existing);
    }
    return Array.from(map.entries()).map(([name, versions]) => {
      const sorted = [...versions].sort((a, b) => b.uploaded_at.localeCompare(a.uploaded_at));
      const latest = sorted[0];
      return {
        chart_name: name,
        latest_version: latest.version,
        latest_app_version: latest.app_version,
        version_count: versions.length,
        total_size: versions.reduce((sum, v) => sum + v.size, 0),
        scan_severity: latest.scan_severity,
        uploaded_at: latest.uploaded_at,
        versions: sorted,
      };
    });
  }, [chartVersions]);

  const filtered = useMemo(() => {
    if (!filter) return groups;
    const q = filter.toLowerCase();
    return groups.filter((g) => g.chart_name.toLowerCase().includes(q));
  }, [groups, filter]);

  const columns: ColumnDef<HelmChartGroup>[] = [
    {
      id: 'chart_name',
      name: 'Chart Name',
      sortable: true,
      render: (row) => (
        <button
          className="inline-flex items-center gap-1.5 text-sm font-medium text-primary hover:underline"
          onClick={() =>
            setExpandedChart(expandedChart === row.chart_name ? null : row.chart_name)
          }
        >
          {expandedChart === row.chart_name ? (
            <ChevronDown className="size-3.5" />
          ) : (
            <ChevronRight className="size-3.5" />
          )}
          <Layers className="size-3.5" />
          {row.chart_name}
        </button>
      ),
    },
    {
      id: 'latest_version',
      name: 'Version',
      sortable: true,
      accessor: (row) => row.latest_version,
    },
    {
      id: 'latest_app_version',
      name: 'App Version',
      sortable: true,
      accessor: (row) => row.latest_app_version || '-',
    },
    {
      id: 'total_size',
      name: 'Size',
      sortable: true,
      accessor: (row) => formatBytes(row.total_size),
    },
    {
      id: 'uploaded_at',
      name: 'Upload Date',
      sortable: true,
      accessor: (row) => formatDate(row.uploaded_at),
    },
    {
      id: 'scan',
      name: 'Scan Status',
      render: (row) =>
        row.scan_severity ? (
          <SeverityBadge severity={row.scan_severity} />
        ) : (
          <span className="text-xs text-muted-foreground">Not scanned</span>
        ),
    },
  ];

  const handleUpload = async (file: File, onProgress: (pct: number) => void) => {
    await api.upload(`/projects/${repo.name}/repos/${repo.name}/artifacts`, file, onProgress);
  };

  return (
    <RepoPageLayout repo={repo}>
      <div className="space-y-4">
        {/* Upload dropzone */}
        <Dropzone onUpload={handleUpload} accept=".tgz,.tar.gz" />

        {/* Filter */}
        <InlineSearch
          value={filter}
          onChange={setFilter}
          placeholder="Filter by chart name..."
          className="max-w-sm"
        />

        {/* Grouped table */}
        <DataTable
          columns={columns}
          data={filtered}
          sort={sort}
          onSort={(col, dir) => setSort({ column: col, direction: dir })}
          emptyMessage="No Helm charts found. Upload a chart .tgz to get started."
        />

        {/* Expanded chart versions */}
        {expandedChart && (() => {
          const group = groups.find((g) => g.chart_name === expandedChart);
          if (!group) return null;
          return (
            <div className="ml-6 rounded-md border bg-muted/30 p-4 space-y-2">
              <h4 className="text-sm font-semibold">
                {group.chart_name} -- {group.version_count} version(s)
              </h4>
              <div className="space-y-1">
                {group.versions.map((v) => (
                  <div
                    key={v.id}
                    className="flex items-center justify-between rounded-md border bg-background px-3 py-1.5 text-xs"
                  >
                    <div className="flex items-center gap-2">
                      <span className="font-mono font-medium">{v.version}</span>
                      {v.app_version && (
                        <span className="text-muted-foreground">app: {v.app_version}</span>
                      )}
                    </div>
                    <div className="flex items-center gap-3">
                      <span className="text-muted-foreground">{formatBytes(v.size)}</span>
                      <span className="text-muted-foreground">{formatDate(v.uploaded_at)}</span>
                      {v.scan_severity ? (
                        <SeverityBadge severity={v.scan_severity} />
                      ) : (
                        <span className="text-muted-foreground">Not scanned</span>
                      )}
                    </div>
                  </div>
                ))}
              </div>
            </div>
          );
        })()}
      </div>
    </RepoPageLayout>
  );
}
