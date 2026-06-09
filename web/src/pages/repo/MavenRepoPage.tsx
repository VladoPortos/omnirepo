/**
 * Maven repository detail page.
 *
 * Flat table of artifact files: group:artifact, version, type
 * (classifier + extension, e.g. "jar" / "sources.jar"), size, upload
 * date, plus per-row file download + delete (maintainer only).
 * Modeled on GoRepoPage with the protocol-specific exclusions:
 *   - NO scan column / rescan buttons — maven artifacts are never
 *     scanned.
 *   - NO sync button or mirror affordances — maven repos have no
 *     mirror/sync support (same as go/raw).
 *   - NO upload dropzone — publishing goes through `mvn deploy` or
 *     Gradle maven-publish (see the Publish snippet); there is no
 *     generic REST upload endpoint for maven.
 *
 * Download + delete both address the file by its repository-layout
 * storage path (extra.path), percent-encoded per segment so the
 * slashes survive as route separators.
 */

import { useState, useMemo } from 'react';
import { useParams } from 'react-router-dom';
import { FolderArchive, Download, Terminal, Trash2 } from 'lucide-react';
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
import { useRepoContent, useDeleteMavenArtifact } from '@/api/queries';
import { useRoleFor } from '@/hooks/useAuth';
import type { Repo } from '@/api/types';

interface MavenArtifactFile {
  /** Coordinates as listed, e.g. com.acme:foo. */
  coords: string;
  version: string;
  /** Optional classifier, e.g. "sources"; empty for the main artifact. */
  classifier: string;
  /** File extension, e.g. "jar", "pom". */
  extension: string;
  /** Plain filename, e.g. foo-1.0.0-sources.jar. */
  filename: string;
  /** Repository-layout storage path (slash-separated) — the delete and
   * download key. */
  path: string;
  size: number;
  uploaded_at: string;
}

interface MavenRepoPageProps {
  repo: Repo;
}

/** "sources.jar" / "jar" — the human-readable file-type chip. */
function fileType(row: MavenArtifactFile): string {
  return row.classifier ? `${row.classifier}.${row.extension}` : row.extension;
}

/** Build the same-origin download path for an artifact file from its
 * repository-layout path. Slashes stay as route separators; every
 * other reserved char is percent-encoded per segment (the shared
 * encode-per-segment pattern from RawRepoPage). */
function filePath(
  projectName: string,
  repoName: string,
  row: MavenArtifactFile,
): string {
  const escapedPath = row.path.split('/').map(encodeURIComponent).join('/');
  return `/${encodeURIComponent(projectName)}/maven/${encodeURIComponent(repoName)}/${escapedPath}`;
}

export function MavenRepoPage({ repo }: MavenRepoPageProps) {
  const { name: projectName } = useParams<{ name: string }>();
  const [filter, setFilter] = useState('');
  const [sort, setSort] = useState<SortState>({ column: 'coords', direction: 'asc' });
  // Row-delete state — one confirm dialog per artifact file.
  const [pendingDelete, setPendingDelete] = useState<MavenArtifactFile | null>(null);
  const [deleteError, setDeleteError] = useState<ApiErrorEnvelope | null>(null);
  const deleteMut = useDeleteMavenArtifact(projectName ?? '', repo.name);

  // Role-aware delete permission gate (mirrors GoRepoPage).
  const myRole = useRoleFor(projectName ?? '');
  const isMaintainer = myRole === 'maintainer';
  const canDelete = isMaintainer;
  const hostname = window.location.host;

  const { data: contentRows } = useRepoContent(projectName ?? '', 'maven', repo.name);

  const rows: MavenArtifactFile[] = useMemo(
    () =>
      (contentRows ?? []).map((row) => {
        const e = (row.extra ?? {}) as Record<string, unknown>;
        return {
          coords: row.name,
          version: row.version ?? '',
          classifier: String(e.classifier ?? ''),
          extension: String(e.extension ?? ''),
          filename: String(e.filename ?? ''),
          path: String(e.path ?? ''),
          size: row.size_bytes,
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
        r.coords.toLowerCase().includes(q) ||
        r.version.toLowerCase().includes(q) ||
        r.filename.toLowerCase().includes(q),
    );
  }, [rows, filter]);

  const columns: ColumnDef<MavenArtifactFile>[] = [
    {
      id: 'coords',
      name: 'Artifact',
      sortable: true,
      render: (row) => (
        <span
          className="inline-flex items-center gap-1.5 text-sm font-medium"
          title={row.filename || undefined}
        >
          <FolderArchive className="size-3.5 text-muted-foreground" />
          <span className="font-mono text-xs">{row.coords}</span>
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
      id: 'type',
      name: 'Type',
      sortable: true,
      accessor: (row) => fileType(row),
      className: 'font-mono text-xs text-muted-foreground',
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
            title="Download artifact file"
            nativeButton={false}
            render={
              <a
                href={filePath(projectName ?? '', repo.name, row)}
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
              title="Delete artifact file"
              className="text-muted-foreground hover:bg-destructive/10 hover:text-destructive"
              onClick={() => {
                setDeleteError(null);
                setPendingDelete(row);
              }}
              disabled={
                deleteMut.isPending && pendingDelete?.path === row.path
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
          placeholder="Filter by artifact, version, or filename..."
          className="max-w-sm"
        />

        {/* Artifact-file table — empty state when nothing deployed yet */}
        {rows.length === 0 ? (
          <EmptyState
            icon={Terminal}
            title="No artifacts yet"
            description="Deploy your first artifact using the snippets below."
          >
            <SnippetList
              repoType="maven"
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
            emptyMessage="No artifact files match the filter."
          />
        )}
      </div>

      {/* Delete-file confirm (wired to
          DELETE /api/v1/projects/{name}/repos/maven/{repo}/{path}). */}
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
            <DialogTitle>Delete artifact file?</DialogTitle>
            <DialogDescription>
              This removes{' '}
              <code className="rounded bg-muted px-1 text-xs">
                {pendingDelete?.filename ||
                  `${pendingDelete?.coords}:${pendingDelete?.version}`}
              </code>{' '}
              from the repository. Builds resolving this file will fail
              afterwards.
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
                  await deleteMut.mutateAsync({ path: pendingDelete.path });
                  toast.success(`Deleted ${pendingDelete.filename || pendingDelete.path}`);
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
