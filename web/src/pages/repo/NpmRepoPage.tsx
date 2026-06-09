/**
 * npm registry repository detail page.
 *
 * Flat table of package versions: package name, version, size, upload
 * date, plus per-row tarball download + delete (maintainer only).
 * Modeled on GoRepoPage with the protocol-specific exclusions:
 *   - NO scan column / rescan buttons — npm artifacts are never scanned.
 *   - NO sync button or mirror affordances — npm repos have no
 *     mirror/sync support (same as go/raw).
 *   - NO upload dropzone — publishing goes through `npm publish` (see
 *     the Publish snippet); there is no generic REST upload endpoint
 *     for npm.
 *
 * URL encoding: the tarball download follows the npm protocol shape
 * (/{project}/npm/{repo}/{name}/-/{tarball}) with the package name
 * encoded per segment so the scope slash in @scope/name survives as a
 * route separator. The row delete (see useDeleteNpmPackageVersion)
 * instead encodes the package name as ONE segment (@scope%2Fname) per
 * the REST API contract.
 */

import { useState, useMemo } from 'react';
import { useParams } from 'react-router-dom';
import { Hexagon, Download, Terminal, Trash2 } from 'lucide-react';
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
import { envelopeFromError, type ApiErrorEnvelope } from '@/api/client';
import { useRepoContent, useDeleteNpmPackageVersion } from '@/api/queries';
import { useRoleFor } from '@/hooks/useAuth';
import type { Repo } from '@/api/types';

interface NpmPackageVersion {
  /** Package name as published, possibly scoped, e.g. @acme/foo. */
  pkg: string;
  version: string;
  size: number;
  description: string;
  /** Tarball filename, e.g. foo-1.0.0.tgz. */
  tarball: string;
  uploaded_at: string;
}

interface NpmRepoPageProps {
  repo: Repo;
}

/** Build the same-origin npm tarball path for a package version. The
 * package name keeps its scope slash as a route separator (the npm
 * protocol URL shape); every other reserved char is percent-encoded
 * per segment. */
function tarballPath(
  projectName: string,
  repoName: string,
  row: NpmPackageVersion,
): string {
  const escapedPkg = row.pkg.split('/').map(encodeURIComponent).join('/');
  return `/${encodeURIComponent(projectName)}/npm/${encodeURIComponent(repoName)}/${escapedPkg}/-/${encodeURIComponent(row.tarball)}`;
}

export function NpmRepoPage({ repo }: NpmRepoPageProps) {
  const { name: projectName } = useParams<{ name: string }>();
  const [filter, setFilter] = useState('');
  const [sort, setSort] = useState<SortState>({ column: 'pkg', direction: 'asc' });
  // Row-delete state — one confirm dialog per package version.
  const [pendingDelete, setPendingDelete] = useState<NpmPackageVersion | null>(null);
  const [deleteError, setDeleteError] = useState<ApiErrorEnvelope | null>(null);
  const deleteMut = useDeleteNpmPackageVersion(projectName ?? '', repo.name);

  // Role-aware delete permission gate (mirrors GoRepoPage).
  const myRole = useRoleFor(projectName ?? '');
  const isMaintainer = myRole === 'maintainer';
  const canDelete = isMaintainer;
  const hostname = window.location.host;

  const { data: contentRows } = useRepoContent(projectName ?? '', 'npm', repo.name);

  const rows: NpmPackageVersion[] = useMemo(
    () =>
      (contentRows ?? []).map((row) => {
        const e = (row.extra ?? {}) as Record<string, unknown>;
        return {
          pkg: row.name,
          version: row.version ?? '',
          size: row.size_bytes,
          description: String(e.description ?? ''),
          tarball: String(e.tarball ?? ''),
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
        r.pkg.toLowerCase().includes(q) ||
        r.version.toLowerCase().includes(q),
    );
  }, [rows, filter]);

  const columns: ColumnDef<NpmPackageVersion>[] = [
    {
      id: 'pkg',
      name: 'Package',
      sortable: true,
      render: (row) => (
        <span
          className="inline-flex items-center gap-1.5 text-sm font-medium"
          title={row.description || undefined}
        >
          <Hexagon className="size-3.5 text-muted-foreground" />
          <span className="font-mono text-xs">{row.pkg}</span>
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
            title="Download tarball"
            nativeButton={false}
            render={
              <a
                href={tarballPath(projectName ?? '', repo.name, row)}
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
              title="Delete package version"
              className="text-muted-foreground hover:bg-destructive/10 hover:text-destructive"
              onClick={() => {
                setDeleteError(null);
                setPendingDelete(row);
              }}
              disabled={
                deleteMut.isPending &&
                pendingDelete?.pkg === row.pkg &&
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
          placeholder="Filter by package name or version..."
          className="max-w-sm"
        />

        {/* Package-version table — empty state when nothing published yet */}
        {rows.length === 0 ? (
          <EmptyState
            icon={Terminal}
            title="No packages yet"
            description="Publish your first package version using the snippets below."
          >
            <SnippetList
              repoType="npm"
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
            emptyMessage="No package versions match the filter."
          />
        )}
      </div>

      {/* Delete-version confirm (wired to
          DELETE /api/v1/projects/{name}/repos/npm/{repo}/{package}/-/{version}). */}
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
            <DialogTitle>Delete package version?</DialogTitle>
            <DialogDescription>
              This removes{' '}
              <code className="rounded bg-muted px-1 text-xs">
                {pendingDelete?.pkg}@{pendingDelete?.version}
              </code>{' '}
              from the registry. The <code>latest</code> dist-tag is
              re-pointed automatically; installs pinned to this version
              will fail to resolve it afterwards.
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
                    pkg: pendingDelete.pkg,
                    version: pendingDelete.version,
                  });
                  toast.success(
                    `Deleted ${pendingDelete.pkg}@${pendingDelete.version}`,
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
