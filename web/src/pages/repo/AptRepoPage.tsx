/**
 * APT/Debian repository detail page per D-12.
 * Sortable table with Name, Version, Arch, Suite, Component, Size, Upload Date, Scan Status.
 * Filter dropdowns for suite and component.
 */

import { useState, useMemo } from 'react';
import { useParams } from 'react-router-dom';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { ExternalLink, Terminal, ShieldAlert, RefreshCw, Loader2 } from 'lucide-react';
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
import { ContentScanBadge } from '@/components/common/ContentScanBadge';
import { InlineSearch } from '@/components/common/InlineSearch';
import { Dropzone } from '@/components/common/Dropzone';
import { FilterChips } from '@/components/common/FilterChips';
import { EmptyState } from '@/components/common/EmptyState';
import { SnippetList } from '@/components/common/SnippetList';
import {
  ArtifactDetail,
  ArtifactDigest,
} from '@/components/common/ArtifactDetail';
import { RepoPageLayout } from './RepoPageLayout';
import { formatBytes, formatDate } from '@/lib/format';
import { api, envelopeFromError, type ApiErrorEnvelope, ApiError } from '@/api/client';
import {
  useRepoContent,
  useMe,
  useRepoScans,
  useRescanArtifact,
  useDeleteDebPackage,
} from '@/api/queries';
import { ErrorEnvelopeRenderer } from '@/components/common/ErrorEnvelope';
import {
  SyncNowButton,
  formatFilterSummary,
} from '@/components/SyncNowButton';
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
  filename: string;
  digest: string;
  section: string;
  maintainer: string;
  depends: string;
  storage_pool_path: string;
  scan_status: string;
  severity_counts: Record<string, number>;
  latest_scan_id?: number;
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
  const [expandedID, setExpandedID] = useState<number | null>(null);
  // F-06.3 row-delete confirm state.
  const [pkgPendingDelete, setPkgPendingDelete] = useState<DebPackage | null>(null);
  const [deleteError, setDeleteError] = useState<ApiErrorEnvelope | null>(null);
  const deletePkgMut = useDeleteDebPackage(projectName ?? '', repo.name);

  // EMPTY-03 upload-permission gate — see DockerRepoPage for rationale.
  const { data: currentUser } = useMe();
  const canUpload = !!currentUser;
  // EMPTY-04 (Phase 7): triggers rescan on the FIRST artifact when the repo
  // has artifacts but no scans yet (RESEARCH Open Question §1 option (b)).
  // A repo-level "scan all" endpoint is deferred to v1.2 alongside HEALTH.
  const canScan = !!currentUser?.is_super_admin || canUpload;
  const hostname = window.location.host;

  const { data: contentRows } = useRepoContent(projectName ?? '', 'deb', repo.name);
  const { data: scansData } = useRepoScans(projectName ?? '', 'deb', repo.name);
  const scansCount = scansData?.length ?? 0;
  const [rescanError, setRescanError] = useState<ApiErrorEnvelope | null>(null);
  const qc = useQueryClient();
  const rescanRow = useRescanArtifact(projectName ?? '', 'deb', repo.name);
  const [rescanningID, setRescanningID] = useState<number | null>(null);
  const handleRescanRow = async (id: number) => {
    if (!id) return;
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
      if (!contentRows || contentRows.length === 0) {
        throw new Error('no artifacts to scan');
      }
      const first = contentRows[0];
      return api.post<void>(
        `/projects/${encodeURIComponent(projectName ?? '')}/repos/deb/${encodeURIComponent(repo.name)}/artifacts/${first.id}/rescan`,
        {},
      );
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['repo-scans', projectName ?? '', 'deb', repo.name] });
      toast.success('Scan queued. Results will appear shortly.');
      setRescanError(null);
    },
    onError: (err) => {
      setRescanError(envelopeFromError(err, 'Failed to start scan.'));
    },
  });
  const packages: DebPackage[] = useMemo(
    () =>
      (contentRows ?? []).map((row) => {
        const e = (row.extra ?? {}) as Record<string, unknown>;
        const counts = (e.severity_counts ?? {}) as Record<string, number>;
        return {
          id: row.id ?? 0,
          name: row.name,
          version: row.version ?? '',
          arch: String(e.architecture ?? ''),
          suite: String(e.suite ?? ''),
          component: String(e.component ?? ''),
          size: row.size_bytes,
          scan_severity: row.scan_severity ?? '',
          uploaded_at: row.uploaded_at,
          filename: String(e.filename ?? ''),
          digest: String(e.digest ?? ''),
          section: String(e.section ?? ''),
          maintainer: String(e.maintainer ?? ''),
          depends: String(e.depends ?? ''),
          storage_pool_path: String(e.storage_pool_path ?? ''),
          scan_status: String(e.scan_status ?? ''),
          severity_counts: counts,
          latest_scan_id: row.latest_scan_id,
        };
      }),
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
    {
      id: 'name',
      name: 'Name',
      sortable: true,
      render: (row) => (
        <button
          className="text-sm font-semibold text-primary hover:underline"
          onClick={() =>
            setExpandedID(expandedID === row.id ? null : row.id)
          }
        >
          {row.name}
        </button>
      ),
    },
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
    await api.upload(
      `/projects/${encodeURIComponent(projectName ?? '')}/repos/deb/${encodeURIComponent(repo.name)}/artifacts`,
      file,
      onProgress,
    );
  };

  return (
    <RepoPageLayout repo={repo}>
      <div className="space-y-4">
        {/* Phase 8 Plan 04: mirror Sync Now affordance — visible only on
            is_mirror=true repos, reads upstream config from the repo row. */}
        {repo.is_mirror && (
          <SyncNowButton
            projectName={projectName ?? ''}
            repoType="deb"
            repoName={repo.name}
            upstreamUrl={repo.mirror_upstream_url}
            filterSummary={formatFilterSummary(repo.mirror_filter_json, 'deb')}
          />
        )}

        {/* Actions */}
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

        {/* Upload dropzone — hidden on mirror repos (uploads are 403'd). */}
        {!repo.is_mirror && (
          <Dropzone onUpload={handleUpload} accept=".deb" />
        )}

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
              title={
                repo.is_mirror ? 'Mirror not yet synced' : 'No artifacts yet'
              }
              // F-06.4 (wt3 batch 06): mirror empty state used to claim
              // "Upload your first artifact using the snippet below" even
              // though uploads to mirrors return 403 repo_is_mirror. The
              // signing-key + apt-source snippets are read-only; retarget
              // the description so the two halves agree.
              description={
                repo.is_mirror
                  ? 'Click Sync now to pull from upstream, then use the snippet below to install from this mirror.'
                  : 'Upload your first artifact using the snippet below.'
              }
            >
              <SnippetList
                repoType="deb"
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
          <DataTable
            columns={columns}
            data={filtered}
            sort={sort}
            onSort={(col, dir) => setSort({ column: col, direction: dir })}
            stickyFirstColumn
            isRowExpanded={(row) => expandedID === row.id}
            renderExpanded={(row) => (
              <ArtifactDetail
                title={`${row.name} ${row.version} (${row.arch})`}
                subtitle={row.suite ? `${row.suite}/${row.component}` : undefined}
                sizeBytes={row.size}
                uploadedAt={row.uploaded_at}
                fields={[
                  { label: 'Filename', value: row.filename },
                  { label: 'Pool path', value: row.storage_pool_path },
                  { label: 'Arch', value: row.arch },
                  ...(row.section
                    ? [{ label: 'Section', value: row.section }]
                    : []),
                  ...(row.maintainer
                    ? [{ label: 'Maintainer', value: row.maintainer }]
                    : []),
                  ...(row.depends
                    ? [
                        {
                          label: 'Depends',
                          value: (
                            <span className="font-mono text-xs">
                              {row.depends}
                            </span>
                          ),
                        },
                      ]
                    : []),
                  {
                    label: 'Digest',
                    value: <ArtifactDigest value={row.digest} />,
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
                downloadURL={
                  row.storage_pool_path
                    ? `/${encodeURIComponent(projectName ?? '')}/deb/${encodeURIComponent(repo.name)}/${row.storage_pool_path}`
                    : undefined
                }
                downloadLabel="Download .deb"
                onDelete={
                  canUpload && row.storage_pool_path
                    ? () => {
                        setDeleteError(null);
                        setPkgPendingDelete(row);
                      }
                    : undefined
                }
                deleteLabel="Delete .deb"
                deletePending={
                  deletePkgMut.isPending &&
                  pkgPendingDelete?.id === row.id
                }
              />
            )}
          />
        )}
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

      {/* F-06.3 Delete-package confirm (wired to
          DELETE /api/v1/projects/{name}/repos/deb/{repo}/pool/*). */}
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
              to the trash and regenerates Packages/Release. Restore from
              Admin → Trash within the retention window.
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
                  await deletePkgMut.mutateAsync(pkgPendingDelete.storage_pool_path);
                  toast.success(`Deleted ${pkgPendingDelete.filename}`);
                  setPkgPendingDelete(null);
                  if (expandedID === pkgPendingDelete.id) {
                    setExpandedID(null);
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
