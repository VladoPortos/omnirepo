/**
 * S3 bucket file manager page per D-13.
 * Prefix drill-down navigation, object listing, upload/download/delete.
 */

import { useState, useMemo } from 'react';
import {
  Folder,
  File as FileIcon,
  ChevronRight,
  Download,
  Trash2,
  ArrowLeft,
  HardDrive,
  Hash,
} from 'lucide-react';
import { toast } from 'sonner';
import { Button } from '@/components/ui/button';
import {
  Breadcrumb,
  BreadcrumbList,
  BreadcrumbItem,
  BreadcrumbLink,
  BreadcrumbSeparator,
  BreadcrumbPage,
} from '@/components/ui/breadcrumb';
import { Card, CardContent } from '@/components/ui/card';
import { DataTable, type ColumnDef, type SortState } from '@/components/common/DataTable';
import { InlineSearch } from '@/components/common/InlineSearch';
import { Dropzone } from '@/components/common/Dropzone';
import { RepoPageLayout } from './RepoPageLayout';
import { formatBytes, formatDate } from '@/lib/format';
import { api } from '@/api/client';
import type { Repo } from '@/api/types';

interface S3Object {
  key: string;
  display_name: string;
  is_prefix: boolean;
  size: number;
  last_modified: string;
  etag: string;
}

interface S3BucketPageProps {
  repo: Repo;
}

export function S3BucketPage({ repo }: S3BucketPageProps) {
  const [prefix, setPrefix] = useState('');
  const [filter, setFilter] = useState('');
  const [sort, setSort] = useState<SortState>({ column: 'key', direction: 'asc' });

  // Objects fetched from API; placeholder
  const objects: S3Object[] = [];
  const totalObjects = 0;
  const totalSize = 0;

  const filtered = useMemo(() => {
    if (!filter) return objects;
    const q = filter.toLowerCase();
    return objects.filter((o) => o.display_name.toLowerCase().includes(q));
  }, [objects, filter]);

  // Split prefix into breadcrumb segments
  const prefixSegments = useMemo(() => {
    if (!prefix) return [];
    return prefix.split('/').filter(Boolean);
  }, [prefix]);

  const navigateTo = (newPrefix: string) => {
    setPrefix(newPrefix);
    setFilter('');
  };

  const navigateUp = () => {
    const parts = prefix.split('/').filter(Boolean);
    parts.pop();
    setPrefix(parts.length > 0 ? parts.join('/') + '/' : '');
    setFilter('');
  };

  const columns: ColumnDef<S3Object>[] = [
    {
      id: 'key',
      name: 'Key',
      sortable: true,
      render: (row) =>
        row.is_prefix ? (
          <button
            className="inline-flex items-center gap-1.5 text-sm font-medium text-primary hover:underline"
            onClick={() => navigateTo(row.key)}
          >
            <Folder className="size-4 text-amber-500" />
            {row.display_name}
            <ChevronRight className="size-3 text-muted-foreground" />
          </button>
        ) : (
          <div className="inline-flex items-center gap-1.5 text-sm">
            <FileIcon className="size-4 text-muted-foreground" />
            {row.display_name}
          </div>
        ),
    },
    {
      id: 'size',
      name: 'Size',
      sortable: true,
      accessor: (row) => (row.is_prefix ? '-' : formatBytes(row.size)),
    },
    {
      id: 'last_modified',
      name: 'Last Modified',
      sortable: true,
      accessor: (row) => (row.is_prefix ? '-' : formatDate(row.last_modified)),
    },
    {
      id: 'etag',
      name: 'ETag',
      accessor: (row) => (row.is_prefix ? '' : row.etag),
      className: 'font-mono text-xs max-w-[120px] truncate',
    },
    {
      id: 'actions',
      name: '',
      className: 'w-20',
      render: (row) =>
        !row.is_prefix ? (
          <div className="flex gap-1">
            <Button variant="ghost" size="icon-xs" title="Download">
              <Download className="size-3.5" />
            </Button>
            <Button
              variant="ghost"
              size="icon-xs"
              title="Delete"
              onClick={() => toast.info('Delete not yet connected.')}
            >
              <Trash2 className="size-3.5" />
            </Button>
          </div>
        ) : null,
    },
  ];

  const handleUpload = async (file: File, onProgress: (pct: number) => void) => {
    const key = prefix ? `${prefix}${file.name}` : file.name;
    await api.upload(`/projects/${repo.name}/repos/${repo.name}/artifacts/${key}`, file, onProgress);
  };

  return (
    <RepoPageLayout repo={repo}>
      <div className="space-y-4">
        {/* Bucket stats */}
        <div className="flex flex-wrap gap-4">
          <Card className="min-w-[140px]">
            <CardContent className="flex items-center gap-2 p-3">
              <Hash className="size-4 text-muted-foreground" />
              <div>
                <p className="text-xs text-muted-foreground">Objects</p>
                <p className="text-lg font-semibold tabular-nums">{totalObjects}</p>
              </div>
            </CardContent>
          </Card>
          <Card className="min-w-[140px]">
            <CardContent className="flex items-center gap-2 p-3">
              <HardDrive className="size-4 text-muted-foreground" />
              <div>
                <p className="text-xs text-muted-foreground">Total Size</p>
                <p className="text-lg font-semibold">{formatBytes(totalSize)}</p>
              </div>
            </CardContent>
          </Card>
        </div>

        {/* Prefix breadcrumb */}
        <div className="flex items-center gap-2">
          {prefix && (
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
                  s3://{repo.name}
                </BreadcrumbLink>
              </BreadcrumbItem>
              {prefixSegments.map((seg, i) => {
                const segPath = prefixSegments.slice(0, i + 1).join('/') + '/';
                const isLast = i === prefixSegments.length - 1;
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

        {/* Search / prefix filter */}
        <InlineSearch
          value={filter}
          onChange={setFilter}
          placeholder="Filter by key..."
          className="max-w-sm"
        />

        {/* Object listing */}
        <DataTable
          columns={columns}
          data={filtered}
          sort={sort}
          onSort={(col, dir) => setSort({ column: col, direction: dir })}
          emptyMessage={
            prefix
              ? 'No objects under this prefix.'
              : 'Bucket is empty. Upload an object to get started.'
          }
        />
      </div>
    </RepoPageLayout>
  );
}
