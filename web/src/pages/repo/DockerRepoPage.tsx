/**
 * Docker repository detail page per D-10.
 * Tag list with scan badges, cosign indicator, pull external, promote/retag.
 */

import { useState, useMemo } from 'react';
import { useParams } from 'react-router-dom';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import {
  Download,
  Loader2,
  RefreshCw,
  Trash2,
  ShieldCheck,
  ShieldX,
  Tag,
  ArrowRightLeft,
  Terminal,
  ShieldAlert,
} from 'lucide-react';
import { toast } from 'sonner';
import { Button } from '@/components/ui/button';
import { Badge } from '@/components/ui/badge';
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
import { CopyButton } from '@/components/common/CopyButton';
import { EmptyState } from '@/components/common/EmptyState';
import { SnippetList } from '@/components/common/SnippetList';
import {
  ArtifactDetail,
  ArtifactDigest,
} from '@/components/common/ArtifactDetail';
import { RepoPageLayout } from './RepoPageLayout';
import { formatBytes, formatDate } from '@/lib/format';
import {
  useMe,
  useRepoContent,
  useRepoScans,
  useRescanArtifact,
  usePromoteDockerTag,
  useDeleteDockerTag,
} from '@/api/queries';
import { api, ApiError, envelopeFromError, type ApiErrorEnvelope } from '@/api/client';
import { ErrorEnvelopeRenderer } from '@/components/common/ErrorEnvelope';
import { CloneImageDialog } from '@/components/CloneImageDialog';
import type { Repo } from '@/api/types';

/** Represents a Docker image tag -- populated from /content API. */
interface DockerTag {
  image: string;
  tag: string;
  digest: string;
  size: number;
  scan_status: string;
  scan_severity: string;
  cosign_signed: boolean;
  pushed_at: string;
  media_type: string;
  layer_count: number;
  severity_counts: Record<string, number>;
  latest_scan_id?: number;
}

interface DockerRepoPageProps {
  repo: Repo;
}

export function DockerRepoPage({ repo }: DockerRepoPageProps) {
  const { name: projectName } = useParams<{ name: string }>();
  const [filter, setFilter] = useState('');
  const [sort, setSort] = useState<SortState>({ column: 'pushed_at', direction: 'desc' });
  const [cloneOpen, setCloneOpen] = useState(false);
  const [promoteOpen, setPromoteOpen] = useState(false);
  const [expandedTag, setExpandedTag] = useState<string | null>(null);

  // F-05.6 — Promote dialog form state. Split `dst_project/dst_repo`
  // from one combined input so the placeholder "project/repo" can keep
  // its familiar shape while validation and the submit payload stay
  // typed separately.
  const [promoteSrcTag, setPromoteSrcTag] = useState('');
  const [promoteDst, setPromoteDst] = useState(''); // "project/repo"
  const [promoteDstTag, setPromoteDstTag] = useState('');
  const [promoteError, setPromoteError] = useState<ApiErrorEnvelope | null>(null);
  const promoteMut = usePromoteDockerTag(projectName ?? '', repo.name);

  // F-05.4 — Delete-tag confirm state. Stores the tag pending deletion so
  // we can surface it in the AlertDialog and in toast outcomes.
  const [tagPendingDelete, setTagPendingDelete] = useState<string | null>(null);
  const [deleteError, setDeleteError] = useState<ApiErrorEnvelope | null>(null);
  const deleteTagMut = useDeleteDockerTag(projectName ?? '', repo.name);


  // EMPTY-03 upload-permission gate. v1.0 ships flat project membership
  // (any member = full access) — if the user can see this authenticated
  // page, they are a project member (or super-admin) and can push to it.
  // Conservatively fall back to super-admin for unauthenticated edge
  // cases; a future role-aware permission resolver can replace this.
  const { data: currentUser } = useMe();
  const canUpload = !!currentUser;
  // EMPTY-04 (Phase 7): triggers rescan on the FIRST artifact when the repo
  // has artifacts but no scans yet (RESEARCH Open Question §1 option (b)).
  // A repo-level "scan all" endpoint is deferred to v1.2 alongside HEALTH.
  const canScan = !!currentUser?.is_super_admin || canUpload;

  const hostname = window.location.host;

  // F-T10: /content now returns one row per (image, tag). Map into the
  // DockerTag shape the table renders. Scan status derives from the
  // unified scan_severity: '' = never scanned, 'scanning'/'failed' carry
  // their own status, everything else is a done scan.
  const { data: artifactRows } = useRepoContent(projectName ?? '', 'docker', repo.name);
  const tags = useMemo<DockerTag[]>(() => {
    if (!artifactRows) return [];
    return artifactRows.map((row) => {
      const extra = (row.extra ?? {}) as Record<string, unknown>;
      const sev = row.scan_severity ?? '';
      let scanStatus: string;
      switch (sev) {
        case '':
          scanStatus = '';
          break;
        case 'scanning':
          scanStatus = 'running';
          break;
        case 'failed':
          scanStatus = 'failed';
          break;
        default:
          scanStatus = 'done';
      }
      return {
        image: String(extra.image ?? ''),
        tag: String(extra.tag ?? row.version ?? row.name),
        digest: String(extra.digest ?? ''),
        size: row.size_bytes,
        scan_status: scanStatus,
        scan_severity: sev === 'scanning' || sev === 'failed' ? '' : sev,
        cosign_signed: false, // surfaced separately once cosign rows are joined
        pushed_at: row.uploaded_at,
        latest_scan_id: row.latest_scan_id,
        media_type: String(extra.media_type ?? ''),
        layer_count: Number(extra.layer_count ?? 0),
        severity_counts:
          (extra.severity_counts as Record<string, number>) ?? {},
      };
    });
  }, [artifactRows]);
  const artifactsCount = tags.length;
  const { data: scansData } = useRepoScans(projectName ?? '', 'docker', repo.name);
  const scansCount = scansData?.length ?? 0;
  const [rescanError, setRescanError] = useState<ApiErrorEnvelope | null>(null);
  const qc = useQueryClient();
  // Per-row rescan. Docker keys on the manifest digest — docker_tags has no
  // integer PK so listDockerContent returns id=0 for every row; the REST
  // endpoint accepts a URL-encoded digest (see commits 6e998b2 + f2a1523).
  const rescanRow = useRescanArtifact(projectName ?? '', 'docker', repo.name);
  const [rescanningDigest, setRescanningDigest] = useState<string | null>(null);
  const handleRescanRow = async (digest: string) => {
    setRescanningDigest(digest);
    try {
      await rescanRow.mutateAsync(digest);
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
      setRescanningDigest(null);
    }
  };
  const rescanMutation = useMutation({
    mutationFn: async () => {
      if (!artifactRows || artifactRows.length === 0) {
        throw new Error('no artifacts to scan');
      }
      // Docker scans key on the manifest digest, not a surrogate DB id
      // (docker_tags has no integer PK — the content endpoint returns
      // id=0 for every row). Use first.extra.digest so the rescan
      // endpoint enqueues `artifact_id=<digest>` instead of the literal
      // string "0" which then fails "manifest 0 not found in repo".
      const first = artifactRows[0];
      const digest = String(
        (first.extra as Record<string, unknown> | undefined)?.digest ?? '',
      );
      if (!digest) {
        throw new Error('no digest on first artifact');
      }
      return api.post<void>(
        `/projects/${encodeURIComponent(projectName ?? '')}/repos/docker/${encodeURIComponent(repo.name)}/artifacts/${encodeURIComponent(digest)}/rescan`,
        {},
      );
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['repo-scans', projectName ?? '', 'docker', repo.name] });
      toast.success('Scan queued. Results will appear shortly.');
      setRescanError(null);
    },
    onError: (err) => {
      setRescanError(envelopeFromError(err, 'Failed to start scan.'));
    },
  });

  const filteredTags = useMemo(() => {
    if (!filter) return tags;
    const q = filter.toLowerCase();
    return tags.filter(
      (t) =>
        t.tag.toLowerCase().includes(q) ||
        t.image.toLowerCase().includes(q) ||
        t.digest.toLowerCase().includes(q),
    );
  }, [tags, filter]);

  const columns: ColumnDef<DockerTag>[] = [
    {
      id: 'tag',
      name: 'Image:Tag',
      sortable: true,
      render: (row) => {
        const label = row.image ? `${row.image}:${row.tag}` : row.tag;
        const key = `${row.image}:${row.tag}`;
        return (
          <button
            className="inline-flex items-center gap-1.5 font-mono text-sm text-primary hover:underline"
            onClick={() => setExpandedTag(expandedTag === key ? null : key)}
          >
            <Tag className="size-3.5" />
            {label}
          </button>
        );
      },
    },
    {
      id: 'size',
      name: 'Image Size',
      sortable: true,
      accessor: (row) => formatBytes(row.size),
    },
    {
      id: 'scan_severity',
      name: 'Scan Status',
      sortable: true,
      render: (row) =>
        row.scan_status === 'done' ? (
          <SeverityBadge severity={row.scan_severity || 'unknown'} />
        ) : row.scan_status === 'pending' || row.scan_status === 'running' ? (
          <Badge variant="outline" className="text-xs">
            <RefreshCw className="mr-1 size-3 animate-spin" />
            Scanning
          </Badge>
        ) : (
          <Badge variant="outline" className="text-xs text-muted-foreground">
            Not scanned
          </Badge>
        ),
    },
    {
      id: 'pushed_at',
      name: 'Push Date',
      sortable: true,
      accessor: (row) => formatDate(row.pushed_at),
    },
    {
      id: 'digest',
      name: 'Digest',
      render: (row) => (
        <span className="font-mono text-xs text-muted-foreground">
          {row.digest.length > 19 ? row.digest.slice(0, 19) + '...' : row.digest}
        </span>
      ),
    },
    {
      id: 'cosign',
      name: 'Signed',
      render: (row) =>
        row.cosign_signed ? (
          <span title="Cosign signed"><ShieldCheck className="size-4 text-teal-500" /></span>
        ) : (
          <span title="Unsigned"><ShieldX className="size-4 text-muted-foreground" /></span>
        ),
    },
    {
      id: 'scan_action',
      name: '',
      className: 'w-28 text-right',
      render: (row) => {
        const busy =
          rescanningDigest === row.digest || row.scan_status === 'running';
        return (
          <Button
            variant="outline"
            size="sm"
            title="Queue a fresh Trivy scan for this image"
            onClick={() => handleRescanRow(row.digest)}
            disabled={busy || !row.digest}
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
    {
      id: 'actions',
      name: '',
      className: 'w-24',
      render: (row) => (
        <div className="flex gap-1">
          <CopyButton
            text={`docker pull ${hostname}/${projectName}/${repo.type}/${repo.name}/${row.image || row.tag}:${row.tag}`}
            className="size-7"
          />
          <button
            className="inline-flex size-7 items-center justify-center rounded-md text-muted-foreground hover:bg-muted hover:text-destructive-foreground hover:bg-destructive/10"
            title="Delete tag"
            onClick={() => {
              setDeleteError(null);
              setTagPendingDelete(row.tag);
            }}
            disabled={deleteTagMut.isPending}
          >
            <Trash2 className="size-3.5" />
          </button>
        </div>
      ),
    },
  ];

  return (
    <RepoPageLayout repo={repo}>
      <div className="space-y-4">
        {/* Actions bar */}
        <div className="flex flex-wrap items-center gap-2">
          <Button variant="outline" size="sm" onClick={() => setCloneOpen(true)}>
            <Download className="mr-1.5 size-4" />
            Pull External
          </Button>
          <Button variant="outline" size="sm" onClick={() => setPromoteOpen(true)}>
            <ArrowRightLeft className="mr-1.5 size-4" />
            Promote / Retag
          </Button>
        </div>

        {/* Filter */}
        <InlineSearch
          value={filter}
          onChange={setFilter}
          placeholder="Filter by tag or digest..."
          className="max-w-sm"
        />

        {/* EMPTY-04: repo has artifacts but no scan results yet */}
        {artifactsCount > 0 && scansCount === 0 && (
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

        {/* Tag table — EMPTY-03 when no artifacts yet */}
        {tags.length === 0 ? (
          canUpload ? (
            <EmptyState
              icon={Terminal}
              title="No artifacts yet"
              description="Upload your first artifact using the snippet below."
            >
              <SnippetList
                repoType="docker"
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
            data={filteredTags}
            sort={sort}
            onSort={(col, dir) => setSort({ column: col, direction: dir })}
            stickyFirstColumn
            // F-T17 follow-up: render the tag detail panel inline as an
            // accordion row immediately under the clicked row rather than
            // letting it float at the bottom of a 3-viewport-long list.
            isRowExpanded={(row) =>
              expandedTag === `${row.image}:${row.tag}`
            }
            renderExpanded={(row) => {
              const ref = row.image ? `${row.image}:${row.tag}` : row.tag;
              return (
                <ArtifactDetail
                  title={`${hostname}/${projectName}/${repo.type}/${repo.name}/${ref}`}
                  subtitle={row.media_type}
                  sizeBytes={row.size}
                  uploadedAt={row.pushed_at}
                  fields={[
                    { label: 'Image', value: row.image || <em>(unset)</em> },
                    { label: 'Tag', value: row.tag },
                    {
                      label: 'Digest',
                      value: <ArtifactDigest value={row.digest} />,
                    },
                    {
                      label: 'Layers',
                      value: row.layer_count.toLocaleString(),
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
                />
              );
            }}
          />
        )}
      </div>

      {/* Pull External dialog (Phase 8 / plan 08-03 — CloneImageDialog
          replaces the previous stub block). */}
      <CloneImageDialog
        open={cloneOpen}
        onClose={() => setCloneOpen(false)}
        projectName={projectName ?? ''}
        repoName={repo.name}
        repoId={repo.id}
      />

      {/* Promote/Retag dialog (F-05.6 — wired to
          POST /api/v1/projects/{src}/repos/docker/{repo}/promote). */}
      <Dialog
        open={promoteOpen}
        onOpenChange={(open) => {
          setPromoteOpen(open);
          if (!open) {
            setPromoteSrcTag('');
            setPromoteDst('');
            setPromoteDstTag('');
            setPromoteError(null);
          }
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Promote / Retag Image</DialogTitle>
            <DialogDescription>
              Copy an image tag to another project or repository.
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-3 py-2">
            <div className="space-y-1.5">
              <Label htmlFor="promote-src-tag">Source tag</Label>
              <Input
                id="promote-src-tag"
                placeholder="latest"
                value={promoteSrcTag}
                onChange={(e) => setPromoteSrcTag(e.target.value)}
                disabled={promoteMut.isPending}
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="promote-dst">Target project/repo</Label>
              <Input
                id="promote-dst"
                placeholder="production/releases"
                value={promoteDst}
                onChange={(e) => setPromoteDst(e.target.value)}
                disabled={promoteMut.isPending}
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="promote-dst-tag">Target tag</Label>
              <Input
                id="promote-dst-tag"
                placeholder="v1.0.0"
                value={promoteDstTag}
                onChange={(e) => setPromoteDstTag(e.target.value)}
                disabled={promoteMut.isPending}
              />
            </div>
            {promoteError && (
              <ErrorEnvelopeRenderer envelope={promoteError} mode="inline" />
            )}
          </div>
          <DialogFooter>
            <Button
              variant="outline"
              onClick={() => setPromoteOpen(false)}
              disabled={promoteMut.isPending}
            >
              Cancel
            </Button>
            <Button
              onClick={async () => {
                setPromoteError(null);
                const slash = promoteDst.indexOf('/');
                if (slash < 1 || slash === promoteDst.length - 1) {
                  setPromoteError({
                    code: 'ui.local',
                    message: 'Target must be in the form "project/repo".',
                    class: 'validation',
                  });
                  return;
                }
                const dstProject = promoteDst.slice(0, slash);
                const dstRepo = promoteDst.slice(slash + 1);
                try {
                  const resp = await promoteMut.mutateAsync({
                    src_tag: promoteSrcTag,
                    dst_project: dstProject,
                    dst_repo: dstRepo,
                    dst_tag: promoteDstTag,
                  });
                  toast.success(
                    `Promoted ${promoteSrcTag} → ${resp.dst_project}/${resp.dst_repo}:${resp.dst_tag}`,
                  );
                  setPromoteOpen(false);
                } catch (err) {
                  setPromoteError(
                    envelopeFromError(err, 'Promote failed.'),
                  );
                }
              }}
              disabled={
                promoteMut.isPending ||
                !promoteSrcTag ||
                !promoteDst ||
                !promoteDstTag
              }
            >
              {promoteMut.isPending ? (
                <Loader2 className="mr-1.5 size-4 animate-spin" />
              ) : (
                <ArrowRightLeft className="mr-1.5 size-4" />
              )}
              {promoteMut.isPending ? 'Promoting…' : 'Promote'}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Delete-tag confirm (F-05.4 — wired to
          DELETE /api/v1/projects/{name}/repos/docker/{repo}/tags/{tag}). */}
      <Dialog
        open={!!tagPendingDelete}
        onOpenChange={(open) => {
          if (!open) {
            setTagPendingDelete(null);
            setDeleteError(null);
          }
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete tag?</DialogTitle>
            <DialogDescription>
              This removes the tag <code className="rounded bg-muted px-1 text-xs">{tagPendingDelete}</code> from{' '}
              <code className="rounded bg-muted px-1 text-xs">{repo.name}</code>. The underlying manifest stays
              referenced for other tags; blob reclamation happens on the next GC
              sweep once its ref-count reaches zero.
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
              onClick={() => setTagPendingDelete(null)}
              disabled={deleteTagMut.isPending}
            >
              Cancel
            </Button>
            <Button
              variant="destructive"
              onClick={async () => {
                if (!tagPendingDelete) return;
                setDeleteError(null);
                try {
                  await deleteTagMut.mutateAsync(tagPendingDelete);
                  toast.success(`Tag "${tagPendingDelete}" deleted`);
                  setTagPendingDelete(null);
                } catch (err) {
                  setDeleteError(envelopeFromError(err, 'Delete failed.'));
                }
              }}
              disabled={deleteTagMut.isPending}
            >
              {deleteTagMut.isPending ? (
                <Loader2 className="mr-1.5 size-4 animate-spin" />
              ) : (
                <Trash2 className="mr-1.5 size-4" />
              )}
              {deleteTagMut.isPending ? 'Deleting…' : 'Delete'}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </RepoPageLayout>
  );
}
