/**
 * RPM repository detail page per D-12.
 * Sortable table with Name, Version, Release, Arch, Size, Upload Date, Scan Status.
 */

import { useState, useMemo } from 'react';
import { useParams } from 'react-router-dom';
import { Upload, ExternalLink } from 'lucide-react';
import { toast } from 'sonner';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
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
import { RepoPageLayout } from './RepoPageLayout';
import { formatBytes, formatDate } from '@/lib/format';
import { api } from '@/api/client';
import { useRepoContent } from '@/api/queries';
import type { Repo } from '@/api/types';

interface RpmPackage {
  id: number;
  name: string;
  version: string;
  release: string;
  arch: string;
  size: number;
  scan_severity: string;
  uploaded_at: string;
}

interface RpmRepoPageProps {
  repo: Repo;
}

export function RpmRepoPage({ repo }: RpmRepoPageProps) {
  const { name: projectName } = useParams<{ name: string }>();
  const [filter, setFilter] = useState('');
  const [sort, setSort] = useState<SortState>({ column: 'name', direction: 'asc' });
  const [syncOpen, setSyncOpen] = useState(false);
  const [selectedPkg, setSelectedPkg] = useState<RpmPackage | null>(null);

  // Fetch live packages from the repo-content endpoint (F-3).
  const { data: contentRows } = useRepoContent(projectName ?? '', 'rpm', repo.name);
  const packages: RpmPackage[] = useMemo(
    () =>
      (contentRows ?? []).map((row) => ({
        id: row.id ?? 0,
        name: row.name,
        version: row.version ?? '',
        release: String(row.extra?.release ?? ''),
        arch: String(row.extra?.arch ?? ''),
        size: row.size_bytes,
        scan_severity: row.scan_severity ?? '',
        uploaded_at: row.uploaded_at,
      })),
    [contentRows],
  );

  const filtered = useMemo(() => {
    if (!filter) return packages;
    const q = filter.toLowerCase();
    return packages.filter((p) => p.name.toLowerCase().includes(q));
  }, [packages, filter]);

  const columns: ColumnDef<RpmPackage>[] = [
    {
      id: 'name',
      name: 'Name',
      sortable: true,
      render: (row) => (
        <button
          className="text-sm font-medium text-primary hover:underline"
          onClick={() => setSelectedPkg(selectedPkg?.id === row.id ? null : row)}
        >
          {row.name}
        </button>
      ),
    },
    { id: 'version', name: 'Version', sortable: true, accessor: (row) => row.version },
    { id: 'release', name: 'Release', sortable: true, accessor: (row) => row.release },
    { id: 'arch', name: 'Arch', sortable: true, accessor: (row) => row.arch },
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
    // Upload endpoint: PUT /projects/{project}/repos/{repo}/artifacts
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
        <Dropzone onUpload={handleUpload} accept=".rpm" />

        {/* Filter */}
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
          emptyMessage="No RPM packages found. Upload an .rpm file to get started."
        />

        {/* Selected package detail */}
        {selectedPkg && (
          <div className="rounded-md border bg-muted/30 p-4 space-y-2">
            <h4 className="font-semibold">{selectedPkg.name}-{selectedPkg.version}-{selectedPkg.release}.{selectedPkg.arch}</h4>
            <p className="text-sm text-muted-foreground">
              Package metadata and scan results will be displayed here.
            </p>
            <Button variant="outline" size="sm">
              <Upload className="mr-1.5 size-4" />
              Download RPM
            </Button>
          </div>
        )}
      </div>

      {/* Sync dialog */}
      <Dialog open={syncOpen} onOpenChange={setSyncOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Sync from URL</DialogTitle>
            <DialogDescription>
              Download RPM packages from an external repository URL.
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-3 py-2">
            <div className="space-y-1.5">
              <Label>Source URL</Label>
              <Input placeholder="https://mirror.example.com/centos/9/BaseOS/x86_64/os/" />
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
