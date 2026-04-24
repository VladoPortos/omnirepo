/**
 * PyPI repository detail page per D-12.
 * Table grouped by normalized project name (expandable to see versions/files).
 * Columns: Name, Version, Size, Upload Date, Requires-Python, Scan Status.
 */

import { useState, useMemo } from 'react';
import { Link, useParams } from 'react-router-dom';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { toast } from 'sonner';
import { ChevronDown, ChevronRight, Package, Terminal, ShieldAlert, RefreshCw, Loader2, Trash2 } from 'lucide-react';
import { DataTable, type ColumnDef, type SortState } from '@/components/common/DataTable';
import { ContentScanBadge } from '@/components/common/ContentScanBadge';
import { Button } from '@/components/ui/button';
import { InlineSearch } from '@/components/common/InlineSearch';
import { Dropzone } from '@/components/common/Dropzone';
import { EmptyState } from '@/components/common/EmptyState';
import { SnippetList } from '@/components/common/SnippetList';
import { SeverityStrip } from '@/components/common/ArtifactDetail';
import { RepoPageLayout } from './RepoPageLayout';
import { formatBytes, formatDate } from '@/lib/format';
import { api, envelopeFromError, type ApiErrorEnvelope, ApiError } from '@/api/client';
import {
  useRepoContent,
  useRepoScans,
  useRescanArtifact,
  useDeletePypiFile,
} from '@/api/queries';
import { useRoleFor } from '@/hooks/useAuth';
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
  DialogFooter,
} from '@/components/ui/dialog';
import { ErrorEnvelopeRenderer } from '@/components/common/ErrorEnvelope';
import {
  SyncNowButton,
  formatFilterSummary,
} from '@/components/SyncNowButton';
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
  scan_status: string;
  severity_counts: Record<string, number>;
  uploaded_at: string;
  latest_scan_id?: number;
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
  scan_status: string;
  severity_counts: Record<string, number>;
  uploaded_at: string;
  files: PypiFile[];
  latest_scan_id?: number;
}

interface PypiRepoPageProps {
  repo: Repo;
}

export function PypiRepoPage({ repo }: PypiRepoPageProps) {
  const { name: projectName } = useParams<{ name: string }>();
  const [filter, setFilter] = useState('');
  const [sort, setSort] = useState<SortState>({ column: 'normalized_name', direction: 'asc' });
  const [expandedProject, setExpandedProject] = useState<string | null>(null);
  // F-07.2 row-delete state — one confirm dialog per file.
  const [filePendingDelete, setFilePendingDelete] = useState<PypiFile | null>(null);
  const [deleteError, setDeleteError] = useState<ApiErrorEnvelope | null>(null);
  const deleteFileMut = useDeletePypiFile(projectName ?? '', repo.name);

  // RBAC-06: role-aware upload/scan permission gates.
  const myRole = useRoleFor(projectName ?? '');
  const isMaintainer = myRole === 'maintainer';
  const canUpload = isMaintainer;
  const canScan = isMaintainer;
  const hostname = window.location.host;

  const { data: contentRows } = useRepoContent(projectName ?? '', 'pypi', repo.name);
  const { data: scansData } = useRepoScans(projectName ?? '', 'pypi', repo.name);
  const scansCount = scansData?.length ?? 0;
  const [rescanError, setRescanError] = useState<ApiErrorEnvelope | null>(null);
  const qc = useQueryClient();
  const rescanRow = useRescanArtifact(projectName ?? '', 'pypi', repo.name);
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
        `/projects/${encodeURIComponent(projectName ?? '')}/repos/pypi/${encodeURIComponent(repo.name)}/artifacts/${first.id}/rescan`,
        {},
      );
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['repo-scans', projectName ?? '', 'pypi', repo.name] });
      toast.success('Scan queued. Results will appear shortly.');
      setRescanError(null);
    },
    onError: (err) => {
      setRescanError(envelopeFromError(err, 'Failed to start scan.'));
    },
  });
  const files: PypiFile[] = useMemo(
    () =>
      (contentRows ?? []).map((row) => {
        const e = (row.extra ?? {}) as Record<string, unknown>;
        return {
          id: row.id ?? 0,
          project_name: row.name,
          normalized_name: row.name,
          version: row.version ?? '',
          filename: String(e.filename ?? ''),
          size: row.size_bytes,
          requires_python: String(e.requires_python ?? ''),
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
        scan_status: latest.scan_status,
        severity_counts: latest.severity_counts,
        uploaded_at: latest.uploaded_at,
        files: sorted,
        latest_scan_id: latest.latest_scan_id,
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
      render: (row) => <ContentScanBadge severity={row.scan_severity} />,
    },
    {
      id: 'scan_action',
      name: '',
      className: 'w-28 text-right',
      render: (row) => {
        const latest = row.files[0];
        const busy = rescanningID === latest.id || row.scan_severity === 'scanning';
        return (
          <Button
            variant="outline"
            size="sm"
            title="Rescan latest file only (expand row for per-file rescan)"
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
      `/projects/${encodeURIComponent(projectName ?? '')}/repos/pypi/${encodeURIComponent(repo.name)}/artifacts`,
      file,
      onProgress,
    );
  };

  return (
    <RepoPageLayout repo={repo}>
      <div className="space-y-4">
        {/* Phase 8 Plan 04: mirror Sync Now affordance. */}
        {repo.is_mirror && (
          <SyncNowButton
            projectName={projectName ?? ''}
            repoType="pypi"
            repoName={repo.name}
            upstreamUrl={repo.mirror_upstream_url}
            filterSummary={formatFilterSummary(repo.mirror_filter_json, 'pypi')}
          />
        )}

        {/* Upload dropzone — hidden on mirror repos. */}
        {!repo.is_mirror && (
          <Dropzone onUpload={handleUpload} accept=".whl,.tar.gz,.zip" />
        )}

        {/* Filter */}
        <InlineSearch
          value={filter}
          onChange={setFilter}
          placeholder="Filter by package name..."
          className="max-w-sm"
        />

        {/* EMPTY-04: repo has artifacts but no scan results yet */}
        {files.length > 0 && scansCount === 0 && (
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

        {/* Grouped table — EMPTY-03 when no artifacts yet */}
        {files.length === 0 ? (
          canUpload ? (
            <EmptyState
              icon={Terminal}
              title="No artifacts yet"
              description="Upload your first artifact using the snippet below."
            >
              <SnippetList
                repoType="pypi"
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
            // Inline accordion — file list renders directly under the
            // clicked project row instead of floating at the bottom.
            isRowExpanded={(row) => expandedProject === row.normalized_name}
            renderExpanded={(row) => (
              <div className="space-y-3">
                <div>
                  <h4 className="font-semibold leading-tight">
                    {row.display_name}
                  </h4>
                  <p className="text-xs text-muted-foreground">
                    {row.file_count} file{row.file_count === 1 ? '' : 's'} ·
                    latest {row.latest_version} ·
                    requires-python {row.requires_python || '—'}
                  </p>
                </div>
                <div className="space-y-1">
                  {row.files.map((f) => (
                    <div
                      key={f.id}
                      className="flex items-center justify-between rounded-md border bg-background px-3 py-1.5 text-xs"
                    >
                      <a
                        className="font-mono text-primary hover:underline"
                        // F-07.3 (wt3): backend only serves wheels/sdists
                        // at `/<project>/pypi/<repo>/packages/<filename>`.
                        // The simple/ tree holds the PEP 503 index pages,
                        // not artefacts; linking inside it 404s the SPA.
                        // Session cookie auths the request same-origin.
                        href={`/${encodeURIComponent(projectName ?? '')}/pypi/${encodeURIComponent(repo.name)}/packages/${encodeURIComponent(f.filename)}`}
                        download={f.filename}
                      >
                        {f.filename}
                      </a>
                      <div className="flex items-center gap-3">
                        <span>{f.version}</span>
                        <span className="text-muted-foreground">
                          {formatBytes(f.size)}
                        </span>
                        <span className="text-muted-foreground">
                          {formatDate(f.uploaded_at)}
                        </span>
                        {f.latest_scan_id && f.scan_status === 'done' && (
                          <Link
                            to={`/projects/${encodeURIComponent(projectName ?? '')}/${encodeURIComponent(repo.type)}/${encodeURIComponent(repo.name)}/scans/${f.latest_scan_id}`}
                            className="inline-flex items-center gap-1 text-xs text-primary hover:underline"
                          >
                            <ShieldAlert className="size-3" />
                            Scan report
                          </Link>
                        )}
                        {canUpload && (
                          <button
                            type="button"
                            className="inline-flex size-6 items-center justify-center rounded-md text-muted-foreground hover:bg-destructive/10 hover:text-destructive"
                            title="Delete file"
                            onClick={() => {
                              setDeleteError(null);
                              setFilePendingDelete(f);
                            }}
                            disabled={
                              deleteFileMut.isPending &&
                              filePendingDelete?.id === f.id
                            }
                          >
                            <Trash2 className="size-3" />
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
                      View full scan report
                    </Button>
                  )}
                </div>
              </div>
            )}
          />
        )}
      </div>

      {/* F-07.2 Delete-file confirm (wired to
          DELETE /api/v1/projects/{name}/repos/pypi/{repo}/packages/{filename}). */}
      <Dialog
        open={!!filePendingDelete}
        onOpenChange={(open) => {
          if (!open) {
            setFilePendingDelete(null);
            setDeleteError(null);
          }
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete file?</DialogTitle>
            <DialogDescription>
              This moves{' '}
              <code className="rounded bg-muted px-1 text-xs">
                {filePendingDelete?.filename}
              </code>{' '}
              to the trash and regenerates the PEP 503 Simple index.
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
              onClick={() => setFilePendingDelete(null)}
              disabled={deleteFileMut.isPending}
            >
              Cancel
            </Button>
            <Button
              variant="destructive"
              onClick={async () => {
                if (!filePendingDelete) return;
                setDeleteError(null);
                try {
                  await deleteFileMut.mutateAsync(filePendingDelete.filename);
                  toast.success(`Deleted ${filePendingDelete.filename}`);
                  setFilePendingDelete(null);
                } catch (err) {
                  setDeleteError(envelopeFromError(err, 'Delete failed.'));
                }
              }}
              disabled={deleteFileMut.isPending}
            >
              {deleteFileMut.isPending ? 'Deleting…' : 'Delete'}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </RepoPageLayout>
  );
}
