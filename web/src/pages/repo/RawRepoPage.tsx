/**
 * RAW file repository detail page per D-14.
 * File browser with directory tree navigation, upload dropzone.
 */

import { useState, useMemo } from 'react';
import { useParams } from 'react-router-dom';
import {
  Folder,
  File as FileIcon,
  ChevronRight,
  Download,
  ArrowLeft,
  Terminal,
  RefreshCw,
  Loader2,
} from 'lucide-react';
import { toast } from 'sonner';
import { Button } from '@/components/ui/button';
import { ContentScanBadge } from '@/components/common/ContentScanBadge';
import {
  Breadcrumb,
  BreadcrumbList,
  BreadcrumbItem,
  BreadcrumbLink,
  BreadcrumbSeparator,
  BreadcrumbPage,
} from '@/components/ui/breadcrumb';
import { DataTable, type ColumnDef, type SortState } from '@/components/common/DataTable';
import { InlineSearch } from '@/components/common/InlineSearch';
import { Dropzone } from '@/components/common/Dropzone';
import { EmptyState } from '@/components/common/EmptyState';
import { SnippetList } from '@/components/common/SnippetList';
import {
  ArtifactDetail,
  ArtifactDigest,
} from '@/components/common/ArtifactDetail';
import { RepoPageLayout } from './RepoPageLayout';
import { formatBytes, formatDate } from '@/lib/format';
import { api, ApiError } from '@/api/client';
import { useRepoContent, useRescanArtifact } from '@/api/queries';
import { useRoleFor } from '@/hooks/useAuth';
import type { Repo } from '@/api/types';

interface RawFileEntry {
  name: string;
  path: string;
  is_dir: boolean;
  size: number;
  content_type: string;
  last_modified: string;
  scan_severity: string;
  sha256: string;
  scan_status: string;
  severity_counts: Record<string, number>;
  latest_scan_id?: number;
}

interface RawRepoPageProps {
  repo: Repo;
}

export function RawRepoPage({ repo }: RawRepoPageProps) {
  const { name: projectName } = useParams<{ name: string }>();
  const [currentPath, setCurrentPath] = useState('');
  const [filter, setFilter] = useState('');
  const [sort, setSort] = useState<SortState>({ column: 'name', direction: 'asc' });
  const [expandedPath, setExpandedPath] = useState<string | null>(null);

  // RBAC-06: role-aware upload permission gate.
  const myRole = useRoleFor(projectName ?? '');
  const isMaintainer = myRole === 'maintainer';
  const canUpload = isMaintainer;
  const hostname = window.location.host;

  const { data: contentRows } = useRepoContent(projectName ?? '', 'raw', repo.name);
  // Per-file rescan. Raw uses the file path as artifact_id (see put.go),
  // so we key busy state on path instead of a numeric id.
  const rescanRow = useRescanArtifact(projectName ?? '', 'raw', repo.name);
  const [rescanningPath, setRescanningPath] = useState<string | null>(null);
  const handleRescanRow = async (path: string) => {
    if (!path) return;
    setRescanningPath(path);
    try {
      await rescanRow.mutateAsync(path);
      toast.success('Scan queued.');
    } catch (err) {
      if (err instanceof ApiError && err.status === 412) {
        toast.error('Trivy database is not installed. See /admin/trivy.');
      } else if (err instanceof ApiError) {
        toast.error(err.detail || 'Rescan failed.');
      } else {
        toast.error('Rescan failed.');
      }
    } finally {
      setRescanningPath(null);
    }
  };
  // RAW content endpoint returns a flat list of paths; the directory-tree
  // view is derived client-side by splitting on "/". Files at the root show
  // up directly; deeper paths collapse into synthetic folder rows.
  const entries: RawFileEntry[] = useMemo(() => {
    const rows = contentRows ?? [];
    const folderSizes = new Map<string, number>();
    const folderLatest = new Map<string, string>();
    const folders = new Set<string>();
    const files: RawFileEntry[] = [];
    const prefix = currentPath ? currentPath.replace(/\/$/, '') + '/' : '';
    for (const row of rows) {
      if (prefix && !row.name.startsWith(prefix)) continue;
      const rest = row.name.slice(prefix.length);
      const slash = rest.indexOf('/');
      const e = (row.extra ?? {}) as Record<string, unknown>;
      if (slash === -1) {
        files.push({
          name: rest,
          path: row.name,
          is_dir: false,
          size: row.size_bytes,
          content_type: String(e.mime ?? ''),
          last_modified: row.uploaded_at,
          scan_severity: row.scan_severity ?? '',
          sha256: String(e.sha256 ?? ''),
          scan_status: String(e.scan_status ?? ''),
          severity_counts:
            (e.severity_counts as Record<string, number>) ?? {},
          latest_scan_id: row.latest_scan_id,
        });
      } else {
        const folder = rest.slice(0, slash);
        folders.add(folder);
        folderSizes.set(folder, (folderSizes.get(folder) ?? 0) + row.size_bytes);
        const prev = folderLatest.get(folder) ?? '';
        if (row.uploaded_at > prev) folderLatest.set(folder, row.uploaded_at);
      }
    }
    const folderRows: RawFileEntry[] = Array.from(folders).map((name) => ({
      name,
      path: prefix + name,
      is_dir: true,
      size: folderSizes.get(name) ?? 0,
      content_type: '',
      last_modified: folderLatest.get(name) ?? '',
      // Folder rows don't carry scan state — aggregating worst-of across
      // children is noisy when a dir has hundreds of files. Leave blank.
      scan_severity: '',
      sha256: '',
      scan_status: '',
      severity_counts: {},
    }));
    return [...folderRows, ...files];
  }, [contentRows, currentPath]);

  const filtered = useMemo(() => {
    if (!filter) return entries;
    const q = filter.toLowerCase();
    return entries.filter((e) => e.name.toLowerCase().includes(q));
  }, [entries, filter]);

  // Split current path into breadcrumb segments
  const pathSegments = useMemo(() => {
    if (!currentPath) return [];
    return currentPath.split('/').filter(Boolean);
  }, [currentPath]);

  const navigateTo = (path: string) => {
    setCurrentPath(path);
    setFilter('');
  };

  const navigateUp = () => {
    const parts = currentPath.split('/').filter(Boolean);
    parts.pop();
    setCurrentPath(parts.join('/'));
    setFilter('');
  };

  const columns: ColumnDef<RawFileEntry>[] = [
    {
      id: 'name',
      name: 'Name',
      sortable: true,
      render: (row) =>
        row.is_dir ? (
          <button
            className="inline-flex items-center gap-1.5 text-sm font-medium text-primary hover:underline"
            onClick={() => navigateTo(row.path)}
          >
            <Folder className="size-4 text-amber-500" />
            {row.name}
            <ChevronRight className="size-3 text-muted-foreground" />
          </button>
        ) : (
          <button
            className="inline-flex items-center gap-1.5 text-sm font-medium text-primary hover:underline"
            onClick={() =>
              setExpandedPath(expandedPath === row.path ? null : row.path)
            }
          >
            <FileIcon className="size-4 text-muted-foreground" />
            {row.name}
          </button>
        ),
    },
    {
      id: 'size',
      name: 'Size',
      sortable: true,
      accessor: (row) => (row.is_dir ? '-' : formatBytes(row.size)),
    },
    {
      id: 'content_type',
      name: 'Content-Type',
      sortable: true,
      accessor: (row) => (row.is_dir ? 'directory' : row.content_type || 'application/octet-stream'),
      className: 'font-mono text-xs',
    },
    {
      id: 'last_modified',
      name: 'Last Modified',
      sortable: true,
      accessor: (row) => formatDate(row.last_modified),
    },
    {
      id: 'scan',
      name: 'Scan Status',
      render: (row) =>
        row.is_dir ? (
          <span className="text-xs text-muted-foreground">—</span>
        ) : (
          <ContentScanBadge severity={row.scan_severity} />
        ),
    },
    {
      id: 'actions',
      name: '',
      className: 'w-24 text-right',
      render: (row) => {
        if (row.is_dir) return null;
        const busy =
          rescanningPath === row.path || row.scan_severity === 'scanning';
        return (
          <div className="inline-flex items-center gap-1">
            <Button
              variant="outline"
              size="sm"
              title="Queue a fresh Trivy scan for this file"
              onClick={() => handleRescanRow(row.path)}
              disabled={busy}
            >
              {busy ? (
                <Loader2 className="mr-1.5 size-3.5 animate-spin" />
              ) : (
                <RefreshCw className="mr-1.5 size-3.5" />
              )}
              {busy ? 'Rescanning…' : 'Rescan'}
            </Button>
            <Button
              variant="ghost"
              size="icon-xs"
              title="Download"
              nativeButton={false}
              render={
                <a
                  // FRONTFIX-02: encode each path segment individually
                  // so reserved URL characters (`#`, `%`, `?`, space,
                  // `+`, `&`, ...) survive round-trip to the backend
                  // routing layer. Naïve interpolation of `row.path`
                  // produced URLs that the browser truncated at `#`
                  // (fragment), the chi router rejected at literal
                  // `%` (invalid percent-encoding when the next two
                  // chars aren't hex), and that lost trailing
                  // segments after `?` (query). Splitting on `/` and
                  // re-joining preserves the slash separators while
                  // letting encodeURIComponent handle every other
                  // reserved char. Mirrors the encode-per-filename
                  // pattern HelmRepoPage and PypiRepoPage already use.
                  href={`/${encodeURIComponent(projectName ?? '')}/raw/${encodeURIComponent(repo.name)}/${row.path
                    .split('/')
                    .map(encodeURIComponent)
                    .join('/')}`}
                  download
                />
              }
            >
              <Download className="size-3.5" />
            </Button>
          </div>
        );
      },
    },
  ];

  const handleUpload = async (file: File, onProgress: (pct: number) => void) => {
    const uploadPath = currentPath ? `${currentPath}/${file.name}` : file.name;
    await api.upload(
      `/projects/${encodeURIComponent(projectName ?? '')}/repos/raw/${encodeURIComponent(repo.name)}/artifacts/${uploadPath}`,
      file,
      onProgress,
    );
  };

  return (
    <RepoPageLayout repo={repo}>
      <div className="space-y-4">
        {/* Path breadcrumb within file tree */}
        <div className="flex items-center gap-2">
          {currentPath && (
            <Button variant="ghost" size="icon-xs" onClick={navigateUp} title="Go up">
              <ArrowLeft className="size-4" />
            </Button>
          )}
          <Breadcrumb>
            <BreadcrumbList>
              <BreadcrumbItem>
                <BreadcrumbLink
                  render={
                    <button
                      className="transition-colors hover:text-foreground"
                      onClick={() => navigateTo('')}
                    />
                  }
                >
                  /
                </BreadcrumbLink>
              </BreadcrumbItem>
              {pathSegments.map((seg, i) => {
                const segPath = pathSegments.slice(0, i + 1).join('/');
                const isLast = i === pathSegments.length - 1;
                return (
                  <span key={segPath} className="contents">
                    <BreadcrumbSeparator />
                    <BreadcrumbItem>
                      {isLast ? (
                        <BreadcrumbPage>{seg}</BreadcrumbPage>
                      ) : (
                        <BreadcrumbLink
                          render={
                            <button
                              className="transition-colors hover:text-foreground"
                              onClick={() => navigateTo(segPath)}
                            />
                          }
                        >
                          {seg}
                        </BreadcrumbLink>
                      )}
                    </BreadcrumbItem>
                  </span>
                );
              })}
            </BreadcrumbList>
          </Breadcrumb>
        </div>

        {/* Upload dropzone */}
        <Dropzone onUpload={handleUpload} />

        {/* Filter */}
        <InlineSearch
          value={filter}
          onChange={setFilter}
          placeholder="Filter files..."
          className="max-w-sm"
        />

        {/* File listing — EMPTY-03 when repo has no artifacts at all.
            Per-subdirectory emptiness keeps the informational inline
            message since snippets don't help within a specific subpath. */}
        {!currentPath && (contentRows?.length ?? 0) === 0 ? (
          canUpload ? (
            <EmptyState
              icon={Terminal}
              title="No artifacts yet"
              description="Upload your first artifact using the snippet below."
            >
              <SnippetList
                repoType="raw"
                projectName={projectName ?? ''}
                repoName={repo.name}
                hostname={hostname}
                className="w-full max-w-2xl"
              />
            </EmptyState>
          ) : (
            <EmptyState
              icon={Terminal}
              title="No artifacts yet"
              description="Ask a maintainer to upload an artifact."
            />
          )
        ) : (
          <DataTable
            columns={columns}
            data={filtered}
            sort={sort}
            onSort={(col, dir) => setSort({ column: col, direction: dir })}
            emptyMessage={
              currentPath
                ? 'This directory is empty.'
                : 'No files found. Upload a file to get started.'
            }
            isRowExpanded={(row) => !row.is_dir && expandedPath === row.path}
            renderExpanded={(row) => (
              <ArtifactDetail
                title={row.path}
                sizeBytes={row.size}
                uploadedAt={row.last_modified}
                fields={[
                  {
                    label: 'Content-Type',
                    value: row.content_type || 'application/octet-stream',
                  },
                  {
                    label: 'SHA-256',
                    value: <ArtifactDigest value={row.sha256} />,
                  },
                ]}
                severity={{
                  status: row.scan_status,
                  counts: row.severity_counts,
                }}
                scanReportURL={
                  row.latest_scan_id
                    ? `/projects/${encodeURIComponent(projectName ?? '')}/${encodeURIComponent(repo.type)}/${encodeURIComponent(repo.name)}/scans/${row.latest_scan_id}`
                    : undefined
                }
                // FRONTFIX-02: per-segment encode (see download
                // button in the actions column above for the full
                // rationale). The expanded ArtifactDetail panel
                // surfaces the same "Download" affordance and was
                // affected by the same `#`, `%`, `?`, space-in-path
                // truncation bug.
                downloadURL={`/${encodeURIComponent(projectName ?? '')}/raw/${encodeURIComponent(repo.name)}/${row.path
                  .split('/')
                  .map(encodeURIComponent)
                  .join('/')}`}
                downloadLabel="Download"
              />
            )}
          />
        )}
      </div>
    </RepoPageLayout>
  );
}
