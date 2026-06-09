/**
 * RPM repository detail page.
 * Sortable table with Name, Version, Release, Arch, Size, Upload Date, Scan Status.
 */

import { useState, useMemo } from 'react';
import { useParams } from 'react-router-dom';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { ExternalLink, Terminal, ShieldAlert, RefreshCw, Loader2 } from 'lucide-react';
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
import { ContentScanBadge } from '@/components/common/ContentScanBadge';
import {
  ArtifactDetail,
  ArtifactDigest,
} from '@/components/common/ArtifactDetail';
import { InlineSearch } from '@/components/common/InlineSearch';
import { Dropzone } from '@/components/common/Dropzone';
import { EmptyState } from '@/components/common/EmptyState';
import { SnippetList } from '@/components/common/SnippetList';
import { RepoPageLayout } from './RepoPageLayout';
import { formatBytes, formatDate } from '@/lib/format';
import { api, envelopeFromError, type ApiErrorEnvelope, ApiError } from '@/api/client';
import {
  useRepoContentLoadMore,
  useRepoScans,
  useRescanArtifact,
  useDeleteRpmPackage,
} from '@/api/queries';
import { useRoleFor } from '@/hooks/useAuth';
import { ErrorEnvelopeRenderer } from '@/components/common/ErrorEnvelope';
import { SyncNowButton } from '@/components/SyncNowButton';
import { formatFilterSummary } from '@/lib/filter-summary';
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
  filename: string;
  digest: string;
  summary: string;
  license: string;
  scan_status: string;
  severity_counts: Record<string, number>;
  latest_scan_id?: number;
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
  // Row-delete: same confirm-dialog pattern as DockerRepoPage.
  const [pkgPendingDelete, setPkgPendingDelete] = useState<RpmPackage | null>(null);
  const [deleteError, setDeleteError] = useState<ApiErrorEnvelope | null>(null);
  const deletePkgMut = useDeleteRpmPackage(projectName ?? '', repo.name);

  // Role-aware upload/scan permission gates.
  const myRole = useRoleFor(projectName ?? '');
  const isMaintainer = myRole === 'maintainer';
  const canUpload = isMaintainer;
  const canScan = isMaintainer;
  const hostname = window.location.host;

  const {
    items: contentRows,
    total: contentTotal,
    hasMore: contentHasMore,
    loadMore: loadMoreContent,
    isFetching: contentFetching,
  } = useRepoContentLoadMore(projectName ?? '', 'rpm', repo.name, 100);
  const { data: scansData } = useRepoScans(projectName ?? '', 'rpm', repo.name);
  const scansCount = scansData?.length ?? 0;
  const [rescanError, setRescanError] = useState<ApiErrorEnvelope | null>(null);
  const qc = useQueryClient();
  // Per-row rescan for the content table. Tracks which row is in-flight
  // so we can show a spinner on just that button and disable only it —
  // mass-disabling the whole column while one RPM rescans would be
  // annoying on repos with many packages.
  const rescanRow = useRescanArtifact(projectName ?? '', 'rpm', repo.name);
  const [rescanningID, setRescanningID] = useState<number | null>(null);
  const handleRescanRow = async (id: number) => {
    setRescanningID(id);
    try {
      await rescanRow.mutateAsync(String(id));
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
      setRescanningID(null);
    }
  };
  const rescanMutation = useMutation({
    mutationFn: async () => {
      if (contentRows.length === 0) {
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
      contentRows.map((row) => {
        const e = (row.extra ?? {}) as Record<string, unknown>;
        const counts = (e.severity_counts ?? {}) as Record<string, number>;
        return {
          id: row.id ?? 0,
          name: row.name,
          version: row.version ?? '',
          release: String(e.release ?? ''),
          arch: String(e.arch ?? ''),
          size: row.size_bytes,
          scan_severity: row.scan_severity ?? '',
          uploaded_at: row.uploaded_at,
          filename: String(e.filename ?? ''),
          digest: String(e.digest ?? ''),
          summary: String(e.summary ?? ''),
          license: String(e.license ?? ''),
          scan_status: String(e.scan_status ?? ''),
          severity_counts: counts,
          latest_scan_id: row.latest_scan_id,
        };
      }),
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
      render: (row) => <ContentScanBadge severity={row.scan_severity} />,
    },
    {
      id: 'scan_action',
      name: '',
      className: 'w-28 text-right',
      render: (row) => {
        const busy = rescanningID === row.id || row.scan_severity === 'scanning';
        return (
          <Button
            variant="outline"
            size="sm"
            title="Queue a fresh Trivy scan for this package"
            onClick={() => handleRescanRow(row.id)}
            disabled={busy || !row.id}
          >
            {busy ? (
              <Loader2 className="mr-1.5 size-3.5 animate-spin" />
            ) : (
              <RefreshCw className="mr-1.5 size-3.5" />
            )}
            {busy ? 'Rescanning…' : 'Rescan'}
          </Button>
        );
      },
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
        {/* Mirror Sync Now affordance. */}
        {repo.is_mirror && (
          <SyncNowButton
            projectName={projectName ?? ''}
            repoType="rpm"
            repoName={repo.name}
            upstreamUrl={repo.mirror_upstream_url}
            filterSummary={formatFilterSummary(repo.mirror_filter_json, 'rpm')}
          />
        )}

        {/* Actions — hidden on mirror repos. */}
        {!repo.is_mirror && (
          <div className="flex flex-wrap items-center gap-2">
            <Button
              variant="outline"
              size="sm"
              onClick={() => setSyncOpen(true)}
            >
              <ExternalLink className="mr-1.5 size-4" />
              Sync from URL
            </Button>
          </div>
        )}

        {/* Upload dropzone — hidden on mirror repos. */}
        {!repo.is_mirror && (
          <Dropzone onUpload={handleUpload} accept=".rpm" />
        )}

        {/* Filter */}
        <InlineSearch
          value={filter}
          onChange={setFilter}
          placeholder="Filter by package name..."
          className="max-w-sm"
        />

        {/* Repo has artifacts but no scan results yet */}
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
                disabledHint: 'Maintainer role required',
              }}
            />
            {rescanError && (
              <ErrorEnvelopeRenderer envelope={rescanError} mode="inline" />
            )}
          </>
        )}

        {/* Package table — empty state when no artifacts yet */}
        {packages.length === 0 ? (
          canUpload ? (
            <EmptyState
              icon={Terminal}
              title={
                repo.is_mirror ? 'Mirror not yet synced' : 'No artifacts yet'
              }
              // On a mirror repo the snippet is
              // pull-only (dnf config) — "Upload your first artifact
              // using the snippet below" misled operators into thinking
              // uploads were allowed. Retarget the description based on
              // is_mirror: sync-hint for mirrors, upload-hint for locals.
              description={
                repo.is_mirror
                  ? 'Click Sync now to pull from upstream, then use the snippet below to install from this mirror.'
                  : 'Upload your first artifact using the snippet below.'
              }
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
              description={
                repo.is_mirror
                  ? 'Ask a maintainer to sync this mirror from upstream.'
                  : 'Ask a maintainer to upload an artifact.'
              }
            />
          )
        ) : (
          <>
            <DataTable
              columns={columns}
              data={filtered}
              sort={sort}
              onSort={(col, dir) => setSort({ column: col, direction: dir })}
              stickyFirstColumn
              // Inline accordion — clicking the Name column toggles
              // selectedPkg, and the detail panel now renders as a
              // full-width row directly below the click target rather than
              // floating at the bottom of the list.
              isRowExpanded={(row) => selectedPkg?.id === row.id}
              renderExpanded={(row) => (
                <ArtifactDetail
                  title={`${row.name}-${row.version}-${row.release}.${row.arch}`}
                  subtitle={row.summary || undefined}
                  sizeBytes={row.size}
                  uploadedAt={row.uploaded_at}
                  fields={[
                    { label: 'Filename', value: row.filename },
                    { label: 'Arch', value: row.arch },
                    {
                      label: 'Digest',
                      value: <ArtifactDigest value={row.digest} />,
                    },
                    ...(row.license
                      ? [{ label: 'License', value: row.license }]
                      : []),
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
                  downloadURL={
                    row.filename
                      ? `/${encodeURIComponent(projectName ?? '')}/rpm/${encodeURIComponent(repo.name)}/packages/${encodeURIComponent(row.filename)}`
                      : undefined
                  }
                  downloadLabel="Download RPM"
                  onDelete={
                    canUpload && row.filename
                      ? () => {
                          setDeleteError(null);
                          setPkgPendingDelete(row);
                        }
                      : undefined
                  }
                  deleteLabel="Delete RPM"
                  deletePending={
                    deletePkgMut.isPending &&
                    pkgPendingDelete?.filename === row.filename
                  }
                />
              )}
            />
            {/* Pagination footer. "Showing N of M" + Load more. Filter
                applies client-side to whatever has been loaded, so total
                counts loaded-window rather than total-filtered rows. */}
            <div className="flex items-center justify-between px-1 pt-2 text-xs text-muted-foreground">
              <span>
                Showing {packages.length.toLocaleString()} of{' '}
                {contentTotal.toLocaleString()}
              </span>
              {contentHasMore && (
                <Button
                  variant="outline"
                  size="sm"
                  onClick={loadMoreContent}
                  disabled={contentFetching}
                >
                  {contentFetching ? (
                    <Loader2 className="mr-1.5 size-3.5 animate-spin" />
                  ) : null}
                  Load more
                </Button>
              )}
            </div>
          </>
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

      {/* Delete-package confirm (wired to
          DELETE /api/v1/projects/{name}/repos/rpm/{repo}/packages/{filename}). */}
      <Dialog
        open={!!pkgPendingDelete}
        onOpenChange={(open) => {
          if (!open) {
            setPkgPendingDelete(null);
            setDeleteError(null);
          }
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete package?</DialogTitle>
            <DialogDescription>
              This moves{' '}
              <code className="rounded bg-muted px-1 text-xs">
                {pkgPendingDelete?.filename}
              </code>{' '}
              to the trash and regenerates repodata. The file can be restored
              from Admin → Trash within the retention window.
            </DialogDescription>
          </DialogHeader>
          {deleteError && (
            <div className="py-2">
              <ErrorEnvelopeRenderer envelope={deleteError} mode="inline" />
            </div>
          )}
          <DialogFooter>
            <Button
              variant="outline"
              onClick={() => setPkgPendingDelete(null)}
              disabled={deletePkgMut.isPending}
            >
              Cancel
            </Button>
            <Button
              variant="destructive"
              onClick={async () => {
                if (!pkgPendingDelete) return;
                setDeleteError(null);
                try {
                  await deletePkgMut.mutateAsync(pkgPendingDelete.filename);
                  toast.success(`Deleted ${pkgPendingDelete.filename}`);
                  setPkgPendingDelete(null);
                  if (selectedPkg?.id === pkgPendingDelete.id) {
                    setSelectedPkg(null);
                  }
                } catch (err) {
                  setDeleteError(envelopeFromError(err, 'Delete failed.'));
                }
              }}
              disabled={deletePkgMut.isPending}
            >
              {deletePkgMut.isPending ? 'Deleting…' : 'Delete'}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </RepoPageLayout>
  );
}
