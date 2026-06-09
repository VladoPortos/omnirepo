/**
 * Helm chart repository detail page.
 * Table grouped by chart name (expandable to see versions).
 * Columns: Chart Name, Version, App Version, Size, Upload Date, Scan Status.
 */

import { useState, useMemo } from 'react';
import { Link, useParams } from 'react-router-dom';
import { ChevronDown, ChevronRight, Layers, ShieldAlert, Terminal, Trash2 } from 'lucide-react';
import { DataTable, type ColumnDef, type SortState } from '@/components/common/DataTable';
import { ContentScanBadge } from '@/components/common/ContentScanBadge';
import { Button } from '@/components/ui/button';
import { RefreshCw, Loader2 } from 'lucide-react';
import { toast } from 'sonner';
import { useRescanArtifact, useDeleteHelmChart } from '@/api/queries';
import { ApiError, envelopeFromError, type ApiErrorEnvelope } from '@/api/client';
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
  DialogFooter,
} from '@/components/ui/dialog';
import { ErrorEnvelopeRenderer } from '@/components/common/ErrorEnvelope';
import { InlineSearch } from '@/components/common/InlineSearch';
import { Dropzone } from '@/components/common/Dropzone';
import { EmptyState } from '@/components/common/EmptyState';
import { SnippetList } from '@/components/common/SnippetList';
import { SeverityStrip } from '@/components/common/ArtifactDetail';
import { RepoPageLayout } from './RepoPageLayout';
import { formatBytes, formatDate } from '@/lib/format';
import { api } from '@/api/client';
import { useRepoContent } from '@/api/queries';
import { useRoleFor } from '@/hooks/useAuth';
import { SyncNowButton } from '@/components/SyncNowButton';
import { formatFilterSummary } from '@/lib/filter-summary';
import type { Repo } from '@/api/types';

interface HelmChartVersion {
  id: number;
  chart_name: string;
  version: string;
  app_version: string;
  description: string;
  filename: string;
  size: number;
  scan_severity: string;
  scan_status: string;
  severity_counts: Record<string, number>;
  uploaded_at: string;
  latest_scan_id?: number;
}

/** Grouped view: one row per chart name. */
interface HelmChartGroup {
  chart_name: string;
  latest_version: string;
  latest_app_version: string;
  description: string;
  version_count: number;
  total_size: number;
  scan_severity: string;
  scan_status: string;
  severity_counts: Record<string, number>;
  uploaded_at: string;
  versions: HelmChartVersion[];
  latest_scan_id?: number;
}

interface HelmRepoPageProps {
  repo: Repo;
}

export function HelmRepoPage({ repo }: HelmRepoPageProps) {
  const { name: projectName } = useParams<{ name: string }>();
  const [filter, setFilter] = useState('');
  const [sort, setSort] = useState<SortState>({ column: 'chart_name', direction: 'asc' });
  const [expandedChart, setExpandedChart] = useState<string | null>(null);
  // Row-delete state — confirm dialog per chart version.
  const [chartPendingDelete, setChartPendingDelete] = useState<HelmChartVersion | null>(null);
  const [deleteError, setDeleteError] = useState<ApiErrorEnvelope | null>(null);
  const deleteChartMut = useDeleteHelmChart(projectName ?? '', repo.name);

  // Role-aware upload permission gate.
  const myRole = useRoleFor(projectName ?? '');
  const isMaintainer = myRole === 'maintainer';
  const canUpload = isMaintainer;
  const hostname = window.location.host;

  const { data: contentRows } = useRepoContent(projectName ?? '', 'helm', repo.name);
  // Per-row rescan: for grouped rows we rescan the latest version, which is
  // what the row's badge summarizes anyway.
  const rescanRow = useRescanArtifact(projectName ?? '', 'helm', repo.name);
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
  const chartVersions: HelmChartVersion[] = useMemo(
    () =>
      (contentRows ?? []).map((row) => {
        const e = (row.extra ?? {}) as Record<string, unknown>;
        return {
          id: row.id ?? 0,
          chart_name: row.name,
          version: row.version ?? '',
          app_version: String(e.app_version ?? ''),
          description: String(e.description ?? ''),
          filename: String(e.filename ?? ''),
          size: row.size_bytes,
          scan_severity: row.scan_severity ?? '',
          scan_status: String(e.scan_status ?? ''),
          severity_counts:
            (e.severity_counts as Record<string, number>) ?? {},
          uploaded_at: row.uploaded_at,
          latest_scan_id: row.latest_scan_id,
        };
      }),
    [contentRows],
  );

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
        description: latest.description,
        version_count: versions.length,
        total_size: versions.reduce((sum, v) => sum + v.size, 0),
        scan_severity: latest.scan_severity,
        scan_status: latest.scan_status,
        severity_counts: latest.severity_counts,
        uploaded_at: latest.uploaded_at,
        versions: sorted,
        latest_scan_id: latest.latest_scan_id,
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
      render: (row) => <ContentScanBadge severity={row.scan_severity} />,
    },
    {
      id: 'scan_action',
      name: '',
      className: 'w-28 text-right',
      render: (row) => {
        const latest = row.versions[0];
        const busy = rescanningID === latest.id || row.scan_severity === 'scanning';
        return (
          <Button
            variant="outline"
            size="sm"
            title="Rescan latest version only (expand to rescan specific versions)"
            onClick={() => handleRescanRow(latest.id)}
            disabled={busy || !latest.id}
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
      `/projects/${encodeURIComponent(projectName ?? '')}/repos/helm/${encodeURIComponent(repo.name)}/artifacts`,
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
            repoType="helm"
            repoName={repo.name}
            upstreamUrl={repo.mirror_upstream_url}
            filterSummary={formatFilterSummary(repo.mirror_filter_json, 'helm')}
          />
        )}

        {/* Upload dropzone — hidden on mirror repos. */}
        {!repo.is_mirror && (
          <Dropzone onUpload={handleUpload} accept=".tgz,.tar.gz" />
        )}

        {/* Filter */}
        <InlineSearch
          value={filter}
          onChange={setFilter}
          placeholder="Filter by chart name..."
          className="max-w-sm"
        />

        {/* Grouped table — empty state when no artifacts yet */}
        {chartVersions.length === 0 ? (
          canUpload ? (
            <EmptyState
              icon={Terminal}
              title={
                repo.is_mirror ? 'Mirror not yet synced' : 'No artifacts yet'
              }
              // On a mirror repo the snippet below is pull-only (helm add
              // repo, helm install). "Upload your first artifact using the
              // snippet below" is misleading because uploads 403 on mirror
              // repos. Mirror the same fix applied to the RPM/APT pages.
              description={
                repo.is_mirror
                  ? 'Click Sync now to pull from upstream, then use the snippet below to install from this mirror.'
                  : 'Upload your first artifact using the snippet below.'
              }
            >
              <SnippetList
                repoType="helm"
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
            isRowExpanded={(row) => expandedChart === row.chart_name}
            renderExpanded={(row) => (
              <div className="space-y-3">
                <div>
                  <h4 className="font-semibold leading-tight">
                    {row.chart_name}
                  </h4>
                  <p className="text-xs text-muted-foreground">
                    {row.version_count} version
                    {row.version_count === 1 ? '' : 's'} · latest{' '}
                    {row.latest_version}
                    {row.latest_app_version
                      ? ` (app ${row.latest_app_version})`
                      : ''}
                    {row.description ? ` — ${row.description}` : ''}
                  </p>
                </div>
                <div className="space-y-1">
                  {row.versions.map((v) => (
                    <div
                      key={v.id}
                      className="flex items-center justify-between rounded-md border bg-background px-3 py-1.5 text-xs"
                    >
                      <div className="flex items-center gap-2">
                        <a
                          className="font-mono font-medium text-primary hover:underline"
                          href={`/${encodeURIComponent(projectName ?? '')}/helm/${encodeURIComponent(repo.name)}/${encodeURIComponent(v.filename)}`}
                        >
                          {v.version}
                        </a>
                        {v.app_version && (
                          <span className="text-muted-foreground">
                            app: {v.app_version}
                          </span>
                        )}
                      </div>
                      <div className="flex items-center gap-3">
                        <span className="text-muted-foreground">
                          {formatBytes(v.size)}
                        </span>
                        <span className="text-muted-foreground">
                          {formatDate(v.uploaded_at)}
                        </span>
                        <ContentScanBadge severity={v.scan_severity} />
                        {v.latest_scan_id && v.scan_status === 'done' && (
                          <Button
                            variant="outline"
                            size="sm"
                            nativeButton={false}
                            render={
                              <Link
                                to={`/projects/${encodeURIComponent(projectName ?? '')}/${encodeURIComponent(repo.type)}/${encodeURIComponent(repo.name)}/scans/${v.latest_scan_id}`}
                              />
                            }
                          >
                            <ShieldAlert className="mr-1.5 size-3.5" />
                            Scan report
                          </Button>
                        )}
                        <Button
                          variant="outline"
                          size="sm"
                          title="Queue a fresh Trivy scan for this version"
                          onClick={() => handleRescanRow(v.id)}
                          disabled={
                            !v.id ||
                            rescanningID === v.id ||
                            v.scan_severity === 'scanning'
                          }
                        >
                          {rescanningID === v.id ? (
                            <Loader2 className="mr-1.5 size-3.5 animate-spin" />
                          ) : (
                            <RefreshCw className="mr-1.5 size-3.5" />
                          )}
                          {rescanningID === v.id ? 'Rescanning…' : 'Rescan'}
                        </Button>
                        {canUpload && (
                          <button
                            type="button"
                            className="inline-flex size-7 items-center justify-center rounded-md text-muted-foreground hover:bg-destructive/10 hover:text-destructive"
                            title="Delete chart version"
                            onClick={() => {
                              setDeleteError(null);
                              setChartPendingDelete(v);
                            }}
                            disabled={
                              deleteChartMut.isPending &&
                              chartPendingDelete?.id === v.id
                            }
                          >
                            <Trash2 className="size-3.5" />
                          </button>
                        )}
                      </div>
                    </div>
                  ))}
                </div>
                <div className="flex flex-wrap items-center gap-2">
                  <SeverityStrip
                    status={row.scan_status}
                    counts={row.severity_counts}
                  />
                  {row.latest_scan_id && row.scan_status === 'done' && (
                    <Button
                      variant="outline"
                      size="sm"
                      nativeButton={false}
                      render={
                        <Link
                          to={`/projects/${encodeURIComponent(projectName ?? '')}/${encodeURIComponent(repo.type)}/${encodeURIComponent(repo.name)}/scans/${row.latest_scan_id}`}
                        />
                      }
                    >
                      <ShieldAlert className="mr-1.5 size-3.5" />
                      View latest scan report
                    </Button>
                  )}
                </div>
              </div>
            )}
          />
        )}
      </div>

      {/* Delete-chart confirm (wired to
          DELETE /api/v1/projects/{name}/repos/helm/{repo}/charts/{filename}). */}
      <Dialog
        open={!!chartPendingDelete}
        onOpenChange={(open) => {
          if (!open) {
            setChartPendingDelete(null);
            setDeleteError(null);
          }
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete chart version?</DialogTitle>
            <DialogDescription>
              This moves{' '}
              <code className="rounded bg-muted px-1 text-xs">
                {chartPendingDelete?.filename}
              </code>{' '}
              to the trash and regenerates{' '}
              <code className="rounded bg-muted px-1 text-xs">index.yaml</code>.
              Restore from Admin → Trash within the retention window.
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
              onClick={() => setChartPendingDelete(null)}
              disabled={deleteChartMut.isPending}
            >
              Cancel
            </Button>
            <Button
              variant="destructive"
              onClick={async () => {
                if (!chartPendingDelete) return;
                setDeleteError(null);
                try {
                  await deleteChartMut.mutateAsync(chartPendingDelete.filename);
                  toast.success(`Deleted ${chartPendingDelete.filename}`);
                  setChartPendingDelete(null);
                } catch (err) {
                  setDeleteError(envelopeFromError(err, 'Delete failed.'));
                }
              }}
              disabled={deleteChartMut.isPending}
            >
              {deleteChartMut.isPending ? 'Deleting…' : 'Delete'}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </RepoPageLayout>
  );
}
