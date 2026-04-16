/**
 * RAW file repository detail page per D-14.
 * File browser with directory tree navigation, upload dropzone.
 */

import { useState, useMemo } from 'react';
import {
  Folder,
  File as FileIcon,
  ChevronRight,
  Download,
  ArrowLeft,
} from 'lucide-react';
import { Button } from '@/components/ui/button';
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
import { RepoPageLayout } from './RepoPageLayout';
import { formatBytes, formatDate } from '@/lib/format';
import { api } from '@/api/client';
import type { Repo } from '@/api/types';

interface RawFileEntry {
  name: string;
  path: string;
  is_dir: boolean;
  size: number;
  content_type: string;
  last_modified: string;
}

interface RawRepoPageProps {
  repo: Repo;
}

export function RawRepoPage({ repo }: RawRepoPageProps) {
  const [currentPath, setCurrentPath] = useState('');
  const [filter, setFilter] = useState('');
  const [sort, setSort] = useState<SortState>({ column: 'name', direction: 'asc' });

  // Files fetched from API; placeholder empty
  const entries: RawFileEntry[] = [];

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
          <div className="inline-flex items-center gap-1.5 text-sm">
            <FileIcon className="size-4 text-muted-foreground" />
            {row.name}
          </div>
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
      id: 'actions',
      name: '',
      className: 'w-16',
      render: (row) =>
        !row.is_dir ? (
          <Button variant="ghost" size="icon-xs" title="Download">
            <Download className="size-3.5" />
          </Button>
        ) : null,
    },
  ];

  const handleUpload = async (file: File, onProgress: (pct: number) => void) => {
    const uploadPath = currentPath ? `${currentPath}/${file.name}` : file.name;
    await api.upload(`/projects/${repo.name}/repos/${repo.name}/artifacts/${uploadPath}`, file, onProgress);
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

        {/* File listing */}
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
        />
      </div>
    </RepoPageLayout>
  );
}
