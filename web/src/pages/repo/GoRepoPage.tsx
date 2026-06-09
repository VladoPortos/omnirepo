/**
 * Go module proxy (GOPROXY) repository detail page.
 *
 * Flat table of module versions: module path, version, size, upload
 * date, plus per-row download + delete (maintainer only). Modeled on
 * the simplest sibling pages (Raw/Pypi) with the protocol-specific
 * exclusions:
 *   - NO scan column / rescan buttons — go artifacts are never scanned.
 *   - NO sync button or mirror affordances — go repos have no
 *     mirror/sync support (same as raw).
 *   - NO upload dropzone — publishing is a curl PUT of a module zip to
 *     the GOPROXY path (see the Publish snippet); there is no generic
 *     REST upload endpoint for go.
 *
 * Download + delete URLs apply the GOPROXY case-escaping rule
 * (uppercase → "!"+lowercase, lib/gomod.ts) to the module path before
 * per-segment percent-encoding.
 */

import { useState, useMemo } from 'react';
import { useParams } from 'react-router-dom';
import { Boxes, Download, Terminal, Trash2 } from 'lucide-react';
import { toast } from 'sonner';
import { Button } from '@/components/ui/button';
import { DataTable, type ColumnDef, type SortState } from '@/components/common/DataTable';
import { InlineSearch } from '@/components/common/InlineSearch';
import { EmptyState } from '@/components/common/EmptyState';
import { SnippetList } from '@/components/common/SnippetList';
import { ErrorEnvelopeRenderer } from '@/components/common/ErrorEnvelope';
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
  DialogFooter,
} from '@/components/ui/dialog';
import { RepoPageLayout } from './RepoPageLayout';
import { formatBytes, formatDate } from '@/lib/format';
import { escapeGoModulePath } from '@/lib/gomod';
import { envelopeFromError, type ApiErrorEnvelope } from '@/api/client';
import { useRepoContent, useDeleteGoModuleVersion } from '@/api/queries';
import { useRoleFor } from '@/hooks/useAuth';
import type { Repo } from '@/api/types';

interface GoModuleVersion {
  /** Module path as published, e.g. github.com/acme/foo. */
  module: string;
  version: string;
  size: number;
  digest: string;
  uploaded_at: string;
}

interface GoRepoPageProps {
  repo: Repo;
}

/** Build the same-origin GOPROXY zip path for a module version. The
 * escaped module path keeps its slashes as route separators; every
 * other reserved char is percent-encoded per segment (the shared
 * encode-per-segment pattern from RawRepoPage). */
function zipPath(
  projectName: string,
  repoName: string,
  row: GoModuleVersion,
): string {
  const escapedModule = escapeGoModulePath(row.module)
    .split('/')
    .map(encodeURIComponent)
    .join('/');
  const escapedVersion = encodeURIComponent(escapeGoModulePath(row.version));
  return `/${encodeURIComponent(projectName)}/go/${encodeURIComponent(repoName)}/${escapedModule}/@v/${escapedVersion}.zip`;
}

export function GoRepoPage({ repo }: GoRepoPageProps) {
  const { name: projectName } = useParams<{ name: string }>();
  const [filter, setFilter] = useState('');
  const [sort, setSort] = useState<SortState>({ column: 'module', direction: 'asc' });
  // Row-delete state — one confirm dialog per module version.
  const [pendingDelete, setPendingDelete] = useState<GoModuleVersion | null>(null);
  const [deleteError, setDeleteError] = useState<ApiErrorEnvelope | null>(null);
  const deleteMut = useDeleteGoModuleVersion(projectName ?? '', repo.name);

  // Role-aware delete permission gate (mirrors PypiRepoPage's canUpload).
  const myRole = useRoleFor(projectName ?? '');
  const isMaintainer = myRole === 'maintainer';
  const canDelete = isMaintainer;
  const hostname = window.location.host;

  const { data: contentRows } = useRepoContent(projectName ?? '', 'go', repo.name);

  const rows: GoModuleVersion[] = useMemo(
    () =>
      (contentRows ?? []).map((row) => {
        const e = (row.extra ?? {}) as Record<string, unknown>;
        return {
          module: row.name,
          version: row.version ?? '',
          size: row.size_bytes,
          digest: String(e.digest ?? ''),
          uploaded_at: row.uploaded_at,
        };
      }),
    [contentRows],
  );

  const filtered = useMemo(() => {
    if (!filter) return rows;
    const q = filter.toLowerCase();
    return rows.filter(
      (r) =>
        r.module.toLowerCase().includes(q) ||
        r.version.toLowerCase().includes(q),
    );
  }, [rows, filter]);

  const columns: ColumnDef<GoModuleVersion>[] = [
    {
      id: 'module',
      name: 'Module',
      sortable: true,
      render: (row) => (
        <span className="inline-flex items-center gap-1.5 text-sm font-medium">
          <Boxes className="size-3.5 text-muted-foreground" />
          <span className="font-mono text-xs">{row.module}</span>
        </span>
      ),
    },
    {
      id: 'version',
      name: 'Version',
      sortable: true,
      accessor: (row) => row.version,
      className: 'font-mono text-xs',
    },
    {
      id: 'size',
      name: 'Size',
      sortable: true,
      accessor: (row) => formatBytes(row.size),
    },
    {
      id: 'uploaded_at',
      name: 'Upload Date',
      sortable: true,
      accessor: (row) => formatDate(row.uploaded_at),
    },
    {
      id: 'actions',
      name: '',
      className: 'w-20 text-right',
      render: (row) => (
        <div className="inline-flex items-center gap-1">
          <Button
            variant="ghost"
            size="icon-xs"
            title="Download module zip"
            nativeButton={false}
            render={
              <a
                href={zipPath(projectName ?? '', repo.name, row)}
                download
              />
            }
          >
            <Download className="size-3.5" />
          </Button>
          {canDelete && (
            <Button
              variant="ghost"
              size="icon-xs"
              title="Delete module version"
              className="text-muted-foreground hover:bg-destructive/10 hover:text-destructive"
              onClick={() => {
                setDeleteError(null);
                setPendingDelete(row);
              }}
              disabled={
                deleteMut.isPending &&
                pendingDelete?.module === row.module &&
                pendingDelete?.version === row.version
              }
            >
              <Trash2 className="size-3.5" />
            </Button>
          )}
        </div>
      ),
    },
  ];

  return (
    <RepoPageLayout repo={repo}>
      <div className="space-y-4">
        {/* Filter */}
        <InlineSearch
          value={filter}
          onChange={setFilter}
          placeholder="Filter by module path or version..."
          className="max-w-sm"
        />

        {/* Module-version table — empty state when nothing published yet */}
        {rows.length === 0 ? (
          <EmptyState
            icon={Terminal}
            title="No modules yet"
            description="Publish your first module version using the snippets below."
          >
            <SnippetList
              repoType="go"
              projectName={projectName ?? ''}
              repoName={repo.name}
              hostname={hostname}
              className="w-full max-w-2xl"
            />
          </EmptyState>
        ) : (
          <DataTable
            columns={columns}
            data={filtered}
            sort={sort}
            onSort={(col, dir) => setSort({ column: col, direction: dir })}
            emptyMessage="No module versions match the filter."
          />
        )}
      </div>

      {/* Delete-version confirm (wired to
          DELETE /api/v1/projects/{name}/repos/go/{repo}/<escaped-module>/@v/<version>). */}
      <Dialog
        open={!!pendingDelete}
        onOpenChange={(open) => {
          if (!open) {
            setPendingDelete(null);
            setDeleteError(null);
          }
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete module version?</DialogTitle>
            <DialogDescription>
              This removes{' '}
              <code className="rounded bg-muted px-1 text-xs">
                {pendingDelete?.module}@{pendingDelete?.version}
              </code>{' '}
              from the module proxy. Builds pinned to this version will
              fail to resolve it afterwards.
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
              onClick={() => setPendingDelete(null)}
              disabled={deleteMut.isPending}
            >
              Cancel
            </Button>
            <Button
              variant="destructive"
              onClick={async () => {
                if (!pendingDelete) return;
                setDeleteError(null);
                try {
                  await deleteMut.mutateAsync({
                    module: pendingDelete.module,
                    version: pendingDelete.version,
                  });
                  toast.success(
                    `Deleted ${pendingDelete.module}@${pendingDelete.version}`,
                  );
                  setPendingDelete(null);
                } catch (err) {
                  setDeleteError(envelopeFromError(err, 'Delete failed.'));
                }
              }}
              disabled={deleteMut.isPending}
            >
              {deleteMut.isPending ? 'Deleting…' : 'Delete'}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </RepoPageLayout>
  );
}
