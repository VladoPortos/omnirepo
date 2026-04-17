/**
 * PyPI repository detail page per D-12.
 * Table grouped by normalized project name (expandable to see versions/files).
 * Columns: Name, Version, Size, Upload Date, Requires-Python, Scan Status.
 */

import { useState, useMemo } from 'react';
import { useParams } from 'react-router-dom';
import { ChevronDown, ChevronRight, Package } from 'lucide-react';
import { DataTable, type ColumnDef, type SortState } from '@/components/common/DataTable';
import { SeverityBadge } from '@/components/common/SeverityBadge';
import { InlineSearch } from '@/components/common/InlineSearch';
import { Dropzone } from '@/components/common/Dropzone';
import { RepoPageLayout } from './RepoPageLayout';
import { formatBytes, formatDate } from '@/lib/format';
import { api } from '@/api/client';
import { useRepoContent } from '@/api/queries';
import type { Repo } from '@/api/types';

interface PypiFile {
  id: number;
  project_name: string;
  normalized_name: string;
  version: string;
  filename: string;
  size: number;
  requires_python: string;
  scan_severity: string;
  uploaded_at: string;
}

/** Grouped view: one row per normalized project name. */
interface PypiProjectGroup {
  normalized_name: string;
  display_name: string;
  latest_version: string;
  file_count: number;
  total_size: number;
  requires_python: string;
  scan_severity: string;
  uploaded_at: string;
  files: PypiFile[];
}

interface PypiRepoPageProps {
  repo: Repo;
}

export function PypiRepoPage({ repo }: PypiRepoPageProps) {
  const { name: projectName } = useParams<{ name: string }>();
  const [filter, setFilter] = useState('');
  const [sort, setSort] = useState<SortState>({ column: 'normalized_name', direction: 'asc' });
  const [expandedProject, setExpandedProject] = useState<string | null>(null);

  const { data: contentRows } = useRepoContent(projectName ?? '', 'pypi', repo.name);
  const files: PypiFile[] = useMemo(
    () =>
      (contentRows ?? []).map((row) => ({
        id: row.id ?? 0,
        project_name: row.name,
        normalized_name: row.name,
        version: row.version ?? '',
        filename: String(row.extra?.filename ?? ''),
        size: row.size_bytes,
        requires_python: String(row.extra?.requires_python ?? ''),
        scan_severity: row.scan_severity ?? '',
        uploaded_at: row.uploaded_at,
      })),
    [contentRows],
  );

  // Group files by normalized project name
  const groups = useMemo<PypiProjectGroup[]>(() => {
    const map = new Map<string, PypiFile[]>();
    for (const f of files) {
      const existing = map.get(f.normalized_name) ?? [];
      existing.push(f);
      map.set(f.normalized_name, existing);
    }
    return Array.from(map.entries()).map(([name, projectFiles]) => {
      const sorted = [...projectFiles].sort((a, b) => b.uploaded_at.localeCompare(a.uploaded_at));
      const latest = sorted[0];
      return {
        normalized_name: name,
        display_name: latest.project_name,
        latest_version: latest.version,
        file_count: projectFiles.length,
        total_size: projectFiles.reduce((sum, f) => sum + f.size, 0),
        requires_python: latest.requires_python,
        scan_severity: latest.scan_severity,
        uploaded_at: latest.uploaded_at,
        files: sorted,
      };
    });
  }, [files]);

  const filtered = useMemo(() => {
    if (!filter) return groups;
    const q = filter.toLowerCase();
    return groups.filter((g) => g.normalized_name.includes(q) || g.display_name.toLowerCase().includes(q));
  }, [groups, filter]);

  const columns: ColumnDef<PypiProjectGroup>[] = [
    {
      id: 'normalized_name',
      name: 'Package',
      sortable: true,
      render: (row) => (
        <button
          className="inline-flex items-center gap-1.5 text-sm font-medium text-primary hover:underline"
          onClick={() =>
            setExpandedProject(expandedProject === row.normalized_name ? null : row.normalized_name)
          }
        >
          {expandedProject === row.normalized_name ? (
            <ChevronDown className="size-3.5" />
          ) : (
            <ChevronRight className="size-3.5" />
          )}
          <Package className="size-3.5" />
          {row.display_name}
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
      id: 'requires_python',
      name: 'Requires-Python',
      accessor: (row) => row.requires_python || '-',
      className: 'font-mono text-xs',
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
        <Dropzone onUpload={handleUpload} accept=".whl,.tar.gz,.zip" />

        {/* Filter */}
        <InlineSearch
          value={filter}
          onChange={setFilter}
          placeholder="Filter by package name..."
          className="max-w-sm"
        />

        {/* Grouped table */}
        <DataTable
          columns={columns}
          data={filtered}
          sort={sort}
          onSort={(col, dir) => setSort({ column: col, direction: dir })}
          emptyMessage="No Python packages found. Upload a wheel or sdist to get started."
          stickyFirstColumn
        />

        {/* Expanded project files */}
        {expandedProject && (() => {
          const group = groups.find((g) => g.normalized_name === expandedProject);
          if (!group) return null;
          return (
            <div className="ml-6 rounded-md border bg-muted/30 p-4 space-y-2">
              <h4 className="text-sm font-semibold">
                {group.display_name} -- {group.file_count} file(s)
              </h4>
              <div className="space-y-1">
                {group.files.map((f) => (
                  <div
                    key={f.id}
                    className="flex items-center justify-between rounded-md border bg-background px-3 py-1.5 text-xs"
                  >
                    <span className="font-mono">{f.filename}</span>
                    <div className="flex items-center gap-3">
                      <span>{f.version}</span>
                      <span className="text-muted-foreground">{formatBytes(f.size)}</span>
                      <span className="text-muted-foreground">{formatDate(f.uploaded_at)}</span>
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
