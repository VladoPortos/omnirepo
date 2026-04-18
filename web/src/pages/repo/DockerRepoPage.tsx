/**
 * Docker repository detail page per D-10.
 * Tag list with scan badges, cosign indicator, pull external, promote/retag.
 */

import { useState, useMemo } from 'react';
import { useParams } from 'react-router-dom';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import {
  Download,
  RefreshCw,
  Trash2,
  ShieldCheck,
  ShieldX,
  Tag,
  Layers,
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
import { RepoPageLayout } from './RepoPageLayout';
import { formatBytes, formatDate } from '@/lib/format';
import { useMe, useRepoContent, useRepoScans } from '@/api/queries';
import { api, envelopeFromError, type ApiErrorEnvelope } from '@/api/client';
import { ErrorEnvelopeRenderer } from '@/components/common/ErrorEnvelope';
import type { Repo } from '@/api/types';

/** Represents a Docker image tag -- populated from API in a real app. */
interface DockerTag {
  tag: string;
  digest: string;
  size: number;
  scan_status: string;
  scan_severity: string;
  cosign_signed: boolean;
  pushed_at: string;
}

// Mock data for demonstration; will be replaced by API calls
const MOCK_TAGS: DockerTag[] = [];

interface DockerRepoPageProps {
  repo: Repo;
}

export function DockerRepoPage({ repo }: DockerRepoPageProps) {
  const { name: projectName } = useParams<{ name: string }>();
  const [filter, setFilter] = useState('');
  const [sort, setSort] = useState<SortState>({ column: 'pushed_at', direction: 'desc' });
  const [pullOpen, setPullOpen] = useState(false);
  const [promoteOpen, setPromoteOpen] = useState(false);
  const [expandedTag, setExpandedTag] = useState<string | null>(null);

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
  const tags = MOCK_TAGS;

  // Fetch real artifacts + scans for EMPTY-04 detection. Docker tags are
  // still rendered from MOCK_TAGS above; artifacts here refer to the
  // /content endpoint which returns per-manifest rows regardless of the
  // mock-tag source, so EMPTY-04 surfaces when the repo has artifacts
  // uploaded but no scans run yet.
  const { data: artifactRows } = useRepoContent(projectName ?? '', 'docker', repo.name);
  const artifactsCount = artifactRows?.length ?? 0;
  const { data: scansData } = useRepoScans(projectName ?? '', 'docker', repo.name);
  const scansCount = scansData?.length ?? 0;
  const [rescanError, setRescanError] = useState<ApiErrorEnvelope | null>(null);
  const qc = useQueryClient();
  const rescanMutation = useMutation({
    mutationFn: async () => {
      if (!artifactRows || artifactRows.length === 0) {
        throw new Error('no artifacts to scan');
      }
      const first = artifactRows[0];
      return api.post<void>(
        `/projects/${encodeURIComponent(projectName ?? '')}/repos/docker/${encodeURIComponent(repo.name)}/artifacts/${first.id}/rescan`,
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
        t.digest.toLowerCase().includes(q),
    );
  }, [tags, filter]);

  const columns: ColumnDef<DockerTag>[] = [
    {
      id: 'tag',
      name: 'Tag',
      sortable: true,
      render: (row) => (
        <button
          className="inline-flex items-center gap-1.5 font-mono text-sm text-primary hover:underline"
          onClick={() => setExpandedTag(expandedTag === row.tag ? null : row.tag)}
        >
          <Tag className="size-3.5" />
          {row.tag}
        </button>
      ),
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
      id: 'actions',
      name: '',
      className: 'w-24',
      render: (row) => (
        <div className="flex gap-1">
          <CopyButton
            text={`docker pull ${hostname}/${repo.name}:${row.tag}`}
            className="size-7"
          />
          <button
            className="inline-flex size-7 items-center justify-center rounded-md text-muted-foreground hover:bg-muted hover:text-foreground"
            title="Delete tag"
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
          <Button variant="outline" size="sm" onClick={() => setPullOpen(true)}>
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
          />
        )}

        {/* Expanded tag detail */}
        {expandedTag && (
          <div className="rounded-md border bg-muted/30 p-4">
            <div className="flex items-center gap-2 text-sm font-semibold">
              <Layers className="size-4" />
              Layer breakdown for {expandedTag}
            </div>
            <p className="mt-2 text-xs text-muted-foreground">
              Layer details will be populated from the OCI manifest API.
            </p>
          </div>
        )}
      </div>

      {/* Pull External dialog */}
      <Dialog open={pullOpen} onOpenChange={setPullOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Pull External Image</DialogTitle>
            <DialogDescription>
              Pull an image from an external registry into this repository.
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-3 py-2">
            <div className="space-y-1.5">
              <Label>Source URL</Label>
              <Input placeholder="docker.io/library/nginx:latest" />
            </div>
            <div className="space-y-1.5">
              <Label>Retag as (optional)</Label>
              <Input placeholder="my-nginx:v1" />
            </div>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setPullOpen(false)}>
              Cancel
            </Button>
            <Button
              onClick={() => {
                toast.info('Pull requested (API not yet connected).');
                setPullOpen(false);
              }}
            >
              <Download className="mr-1.5 size-4" />
              Pull
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Promote/Retag dialog */}
      <Dialog open={promoteOpen} onOpenChange={setPromoteOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Promote / Retag Image</DialogTitle>
            <DialogDescription>
              Copy an image tag to another project or repository.
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-3 py-2">
            <div className="space-y-1.5">
              <Label>Source tag</Label>
              <Input placeholder="latest" />
            </div>
            <div className="space-y-1.5">
              <Label>Target project/repo</Label>
              <Input placeholder="production/releases" />
            </div>
            <div className="space-y-1.5">
              <Label>Target tag</Label>
              <Input placeholder="v1.0.0" />
            </div>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setPromoteOpen(false)}>
              Cancel
            </Button>
            <Button
              onClick={() => {
                toast.info('Promote requested (API not yet connected).');
                setPromoteOpen(false);
              }}
            >
              <ArrowRightLeft className="mr-1.5 size-4" />
              Promote
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </RepoPageLayout>
  );
}
