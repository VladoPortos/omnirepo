/**
 * RPM repository detail page per D-12.
 * Sortable table with Name, Version, Release, Arch, Size, Upload Date, Scan Status.
 */

import { useState, useMemo } from 'react';
import { useParams } from 'react-router-dom';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { Upload, ExternalLink, Terminal, ShieldAlert } from 'lucide-react';
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
import { EmptyState } from '@/components/common/EmptyState';
import { SnippetList } from '@/components/common/SnippetList';
import { RepoPageLayout } from './RepoPageLayout';
import { formatBytes, formatDate } from '@/lib/format';
import { api, envelopeFromError, type ApiErrorEnvelope } from '@/api/client';
import { useRepoContent, useMe, useRepoScans } from '@/api/queries';
import { ErrorEnvelopeRenderer } from '@/components/common/ErrorEnvelope';
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
  // EMPTY-03 upload-permission gate — see DockerRepoPage for rationale.
  const { data: currentUser } = useMe();
  const canUpload = !!currentUser;
  // EMPTY-04 (Phase 7): triggers rescan on the FIRST artifact when the repo
  // has artifacts but no scans yet (RESEARCH Open Question §1 option (b)).
  // A repo-level "scan all" endpoint is deferred to v1.2 alongside HEALTH.
  const canScan = !!currentUser?.is_super_admin || canUpload;
  const hostname = window.location.host;

  const { data: contentRows } = useRepoContent(projectName ?? '', 'rpm', repo.name);
  const { data: scansData } = useRepoScans(projectName ?? '', 'rpm', repo.name);
  const scansCount = scansData?.length ?? 0;
  const [rescanError, setRescanError] = useState<ApiErrorEnvelope | null>(null);
  const qc = useQueryClient();
  const rescanMutation = useMutation({
    mutationFn: async () => {
      if (!contentRows || contentRows.length === 0) {
        throw new Error('no artifacts to scan');
      }
      const first = contentRows[0];
      return api.post<void>(
        `/projects/${encodeURIComponent(projectName ?? '')}/repos/rpm/${encodeURIComponent(repo.name)}/artifacts/${first.id}/rescan`,
        {},
      );
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['repo-scans', projectName ?? '', 'rpm', repo.name] });
      toast.success('Scan queued. Results will appear shortly.');
      setRescanError(null);
    },
    onError: (err) => {
      setRescanError(envelopeFromError(err, 'Failed to start scan.'));
    },
  });
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
    // Upload endpoint: PUT /projects/{project}/repos/{type}/{repo}/artifacts
    await api.upload(
      `/projects/${encodeURIComponent(projectName ?? '')}/repos/rpm/${encodeURIComponent(repo.name)}/artifacts`,
      file,
      onProgress,
    );
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

        {/* EMPTY-04: repo has artifacts but no scan results yet */}
        {packages.length > 0 && scansCount === 0 && (
          <>
            <EmptyState
              icon={ShieldAlert}
              title="No scan results yet"
              description="Run a scan to see vulnerability findings for this repository."
              primaryCTA={{
                label: 'Run first scan',
                onClick: () => rescanMutation.mutate(),
                disabled: !canScan || rescanMutation.isPending,
                disabledHint: 'Requires maintainer role on this repo',
              }}
            />
            {rescanError && (
              <ErrorEnvelopeRenderer envelope={rescanError} mode="inline" />
            )}
          </>
        )}

        {/* Package table — EMPTY-03 when no artifacts yet */}
        {packages.length === 0 ? (
          canUpload ? (
            <EmptyState
              icon={Terminal}
              title="No artifacts yet"
              description="Upload your first artifact using the snippet below."
            >
              <SnippetList
                repoType="rpm"
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
            stickyFirstColumn
          />
        )}

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
