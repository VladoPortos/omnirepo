/**
 * APT/Debian repository detail page per D-12.
 * Sortable table with Name, Version, Arch, Suite, Component, Size, Upload Date, Scan Status.
 * Filter dropdowns for suite and component.
 */

import { useState, useMemo } from 'react';
import { useParams } from 'react-router-dom';
import { ExternalLink } from 'lucide-react';
import { toast } from 'sonner';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { Badge } from '@/components/ui/badge';
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
  DialogFooter,
} from '@/components/ui/dialog';
import { DataTable, type ColumnDef, type SortState } from '@/components/common/DataTable';
import { SeverityBadge } from '@/components/common/SeverityBadge';
import { InlineSearch } from '@/components/common/InlineSearch';
import { Dropzone } from '@/components/common/Dropzone';
import { FilterChips } from '@/components/common/FilterChips';
import { RepoPageLayout } from './RepoPageLayout';
import { formatBytes, formatDate } from '@/lib/format';
import { api } from '@/api/client';
import { useRepoContent } from '@/api/queries';
import type { Repo } from '@/api/types';

interface DebPackage {
  id: number;
  name: string;
  version: string;
  arch: string;
  suite: string;
  component: string;
  size: number;
  scan_severity: string;
  uploaded_at: string;
}

interface AptRepoPageProps {
  repo: Repo;
}

export function AptRepoPage({ repo }: AptRepoPageProps) {
  const { name: projectName } = useParams<{ name: string }>();
  const [filter, setFilter] = useState('');
  const [sort, setSort] = useState<SortState>({ column: 'name', direction: 'asc' });
  const [suiteFilter, setSuiteFilter] = useState<string[]>([]);
  const [componentFilter, setComponentFilter] = useState<string[]>([]);
  const [syncOpen, setSyncOpen] = useState(false);

  const { data: contentRows } = useRepoContent(projectName ?? '', 'deb', repo.name);
  const packages: DebPackage[] = useMemo(
    () =>
      (contentRows ?? []).map((row) => ({
        id: row.id ?? 0,
        name: row.name,
        version: row.version ?? '',
        arch: String(row.extra?.architecture ?? ''),
        suite: String(row.extra?.suite ?? ''),
        component: String(row.extra?.component ?? ''),
        size: row.size_bytes,
        scan_severity: row.scan_severity ?? '',
        uploaded_at: row.uploaded_at,
      })),
    [contentRows],
  );

  // Derive available suites and components from packages
  const suiteOptions = useMemo(
    () => [...new Set(packages.map((p) => p.suite))].sort().map((s) => ({ label: s, value: s })),
    [packages],
  );
  const componentOptions = useMemo(
    () => [...new Set(packages.map((p) => p.component))].sort().map((c) => ({ label: c, value: c })),
    [packages],
  );

  const filtered = useMemo(() => {
    let result = packages;
    if (suiteFilter.length > 0) result = result.filter((p) => suiteFilter.includes(p.suite));
    if (componentFilter.length > 0) result = result.filter((p) => componentFilter.includes(p.component));
    if (filter) {
      const q = filter.toLowerCase();
      result = result.filter((p) => p.name.toLowerCase().includes(q));
    }
    return result;
  }, [packages, filter, suiteFilter, componentFilter]);

  const columns: ColumnDef<DebPackage>[] = [
    { id: 'name', name: 'Name', sortable: true, accessor: (row) => row.name },
    { id: 'version', name: 'Version', sortable: true, accessor: (row) => row.version },
    { id: 'arch', name: 'Arch', sortable: true, accessor: (row) => row.arch },
    {
      id: 'suite',
      name: 'Suite',
      sortable: true,
      render: (row) => <Badge variant="outline" className="text-xs">{row.suite}</Badge>,
    },
    {
      id: 'component',
      name: 'Component',
      sortable: true,
      render: (row) => <Badge variant="outline" className="text-xs">{row.component}</Badge>,
    },
    { id: 'size', name: 'Size', sortable: true, accessor: (row) => formatBytes(row.size) },
    { id: 'uploaded_at', name: 'Upload Date', sortable: true, accessor: (row) => formatDate(row.uploaded_at) },
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
        {/* Actions */}
        <div className="flex flex-wrap items-center gap-2">
          <Button variant="outline" size="sm" onClick={() => setSyncOpen(true)}>
            <ExternalLink className="mr-1.5 size-4" />
            Sync from URL
          </Button>
        </div>

        {/* Upload dropzone */}
        <Dropzone onUpload={handleUpload} accept=".deb" />

        {/* Suite/Component filters */}
        <div className="flex flex-wrap gap-4">
          {suiteOptions.length > 0 && (
            <div className="space-y-1">
              <Label className="text-xs text-muted-foreground">Suite</Label>
              <FilterChips
                options={suiteOptions}
                selected={suiteFilter}
                onChange={setSuiteFilter}
              />
            </div>
          )}
          {componentOptions.length > 0 && (
            <div className="space-y-1">
              <Label className="text-xs text-muted-foreground">Component</Label>
              <FilterChips
                options={componentOptions}
                selected={componentFilter}
                onChange={setComponentFilter}
              />
            </div>
          )}
        </div>

        {/* Name filter */}
        <InlineSearch
          value={filter}
          onChange={setFilter}
          placeholder="Filter by package name..."
          className="max-w-sm"
        />

        {/* Package table */}
        <DataTable
          columns={columns}
          data={filtered}
          sort={sort}
          onSort={(col, dir) => setSort({ column: col, direction: dir })}
          emptyMessage="No .deb packages found. Upload a package to get started."
        />
      </div>

      {/* Sync dialog */}
      <Dialog open={syncOpen} onOpenChange={setSyncOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Sync from URL</DialogTitle>
            <DialogDescription>
              Download Debian packages from an external repository URL.
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-3 py-2">
            <div className="space-y-1.5">
              <Label>Source URL</Label>
              <Input placeholder="https://deb.example.com/ubuntu jammy main" />
            </div>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setSyncOpen(false)}>
              Cancel
            </Button>
            <Button
              onClick={() => {
                toast.info('Sync requested (API not yet connected).');
                setSyncOpen(false);
              }}
            >
              Sync
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </RepoPageLayout>
  );
}
