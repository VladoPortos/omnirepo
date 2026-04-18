/**
 * S3 bucket detail + object browser.
 *
 * Replaces the placeholder that assumed buckets were rows in the `repos`
 * table. Buckets are project-scoped first-class entities served by
 * /api/v1/projects/{name}/s3-buckets. Object listing hits the new
 * /{bucket}/objects endpoint added in the 2026-04-17 walkthrough session.
 *
 * Uploads/deletes of individual objects go through the S3 SDK (SigV4).
 * The REST layer deliberately doesn't accept object writes — that's what
 * the S3 access-key flow is for.
 */

import { useMemo, useState } from 'react';
import { Link, useParams } from 'react-router-dom';
import { toast } from 'sonner';
import {
  Folder,
  File as FileIcon,
  ChevronRight,
  ArrowLeft,
  HardDrive,
  Hash,
  Trash2,
  Terminal,
} from 'lucide-react';
import { Button } from '@/components/ui/button';
import {
  Breadcrumb,
  BreadcrumbList,
  BreadcrumbItem,
  BreadcrumbLink,
  BreadcrumbSeparator,
  BreadcrumbPage,
} from '@/components/ui/breadcrumb';
import { Card, CardContent } from '@/components/ui/card';
import { Skeleton } from '@/components/ui/skeleton';
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogFooter,
} from '@/components/ui/dialog';
import { InlineSearch } from '@/components/common/InlineSearch';
import { EmptyState } from '@/components/common/EmptyState';
import { SnippetList } from '@/components/common/SnippetList';
import { NotFoundPage } from '@/pages/NotFoundPage';
import {
  useBucket,
  useBucketObjects,
  useDeleteBucket,
  useMe,
} from '@/api/queries';
import { formatBytes, formatDate } from '@/lib/format';
import { envelopeFromError, type ApiErrorEnvelope } from '@/api/client';
import { ErrorEnvelopeRenderer } from '@/components/common/ErrorEnvelope';
import { useNavigate } from 'react-router-dom';

// Row represents one element rendered in the object/prefix table. For a
// prefix row, `size` is the sum of object sizes under that prefix (local to
// the current page) and `key` is the full prefix so navigation is trivial.
interface Row {
  key: string;
  display: string;
  isPrefix: boolean;
  size: number;
  lastModified: string;
  etag: string;
}

// Derive the rows shown at `prefix` from a flat list of objects. We split on
// the next '/' after `prefix` and fold everything after it into prefix rows.
function foldPrefixes(
  items: { key: string; size_bytes: number; last_modified: string; etag: string }[],
  prefix: string,
): Row[] {
  const folders = new Map<string, { size: number; mostRecent: string }>();
  const files: Row[] = [];
  for (const o of items) {
    const rel = o.key.slice(prefix.length);
    const idx = rel.indexOf('/');
    if (idx < 0) {
      files.push({
        key: o.key,
        display: rel,
        isPrefix: false,
        size: o.size_bytes,
        lastModified: o.last_modified,
        etag: o.etag,
      });
    } else {
      const folder = rel.slice(0, idx);
      const cur = folders.get(folder) ?? { size: 0, mostRecent: '' };
      cur.size += o.size_bytes;
      if (o.last_modified > cur.mostRecent) cur.mostRecent = o.last_modified;
      folders.set(folder, cur);
    }
  }
  const folderRows: Row[] = [...folders.entries()].map(([name, v]) => ({
    key: prefix + name + '/',
    display: name + '/',
    isPrefix: true,
    size: v.size,
    lastModified: v.mostRecent,
    etag: '',
  }));
  return [
    ...folderRows.sort((a, b) => a.display.localeCompare(b.display)),
    ...files.sort((a, b) => a.display.localeCompare(b.display)),
  ];
}

export function S3BucketPage() {
  const { name = '', bucket = '' } = useParams<{
    name: string;
    bucket: string;
  }>();
  const navigate = useNavigate();

  const [prefix, setPrefix] = useState('');
  const [filter, setFilter] = useState('');
  const [deleteOpen, setDeleteOpen] = useState(false);
  const [deleteError, setDeleteError] = useState<ApiErrorEnvelope | null>(null);

  // EMPTY-03 upload-permission gate — see DockerRepoPage for rationale.
  const { data: currentUser } = useMe();
  const canUpload = !!currentUser;
  const hostname = window.location.host;

  const bucketQ = useBucket(name, bucket);
  // Fetch ALL objects under `prefix` — the folding logic needs every row to
  // compute accurate per-prefix size. maxKeys=1000 is the server cap; for
  // larger buckets we'd paginate and stream, but that's a future fix.
  const objectsQ = useBucketObjects(name, bucket, {
    prefix,
    limit: 1000,
  });
  const deleteBucket = useDeleteBucket(name);

  const rows = useMemo(() => {
    const items = objectsQ.data?.items ?? [];
    return foldPrefixes(items, prefix);
  }, [objectsQ.data, prefix]);

  const filtered = useMemo(() => {
    if (!filter) return rows;
    const q = filter.toLowerCase();
    return rows.filter((r) => r.display.toLowerCase().includes(q));
  }, [rows, filter]);

  const prefixSegments = useMemo(
    () => prefix.split('/').filter(Boolean),
    [prefix],
  );

  const navigateTo = (newPrefix: string) => {
    setPrefix(newPrefix);
    setFilter('');
  };

  const navigateUp = () => {
    const parts = prefix.split('/').filter(Boolean);
    parts.pop();
    setPrefix(parts.length > 0 ? parts.join('/') + '/' : '');
    setFilter('');
  };

  const onDeleteBucket = async () => {
    setDeleteError(null);
    try {
      await deleteBucket.mutateAsync(bucket);
      toast.success(`Bucket "${bucket}" deleted.`);
      navigate(`/projects/${name}`);
    } catch (err) {
      setDeleteError(envelopeFromError(err, 'Failed to delete bucket.'));
    }
  };

  if (bucketQ.isError) {
    return <NotFoundPage />;
  }

  return (
    <div className="space-y-6">
      {/* Header (outer AppShell breadcrumb already covers nav context) */}
      <div className="flex items-start justify-between gap-4">
        <div>
          <h1 className="text-[28px] font-semibold leading-tight">{bucket}</h1>
          <p className="mt-1 text-sm text-muted-foreground">
            S3 bucket in <Link className="underline" to={`/projects/${name}`}>{name}</Link>.
            Endpoint: <code className="font-mono">/s3/{bucket}</code>
          </p>
        </div>
        <Button
          variant="ghost"
          size="sm"
          className="text-destructive hover:bg-destructive/10"
          onClick={() => {
            setDeleteError(null);
            setDeleteOpen(true);
          }}
        >
          <Trash2 className="mr-1.5 size-4" />
          Delete Bucket
        </Button>
      </div>

      {/* Stats */}
      <div className="flex flex-wrap gap-4">
        <Card className="min-w-[160px]">
          <CardContent className="flex items-center gap-2 p-3">
            <Hash className="size-4 text-muted-foreground" />
            <div>
              <p className="text-xs text-muted-foreground">Objects</p>
              <div className="text-lg font-semibold tabular-nums">
                {bucketQ.isLoading ? (
                  <Skeleton className="h-5 w-12" />
                ) : (
                  bucketQ.data?.object_count ?? 0
                )}
              </div>
            </div>
          </CardContent>
        </Card>
        <Card className="min-w-[160px]">
          <CardContent className="flex items-center gap-2 p-3">
            <HardDrive className="size-4 text-muted-foreground" />
            <div>
              <p className="text-xs text-muted-foreground">Total Size</p>
              <div className="text-lg font-semibold">
                {bucketQ.isLoading ? (
                  <Skeleton className="h-5 w-16" />
                ) : (
                  formatBytes(bucketQ.data?.size_bytes ?? 0)
                )}
              </div>
            </div>
          </CardContent>
        </Card>
      </div>

      {/* Prefix breadcrumb */}
      <div className="flex items-center gap-2">
        {prefix && (
          <Button variant="ghost" size="icon-xs" onClick={navigateUp} title="Go up">
            <ArrowLeft className="size-4" />
          </Button>
        )}
        <Breadcrumb>
          <BreadcrumbList>
            <BreadcrumbItem>
              <BreadcrumbLink
                render={
                  <button
                    type="button"
                    className="transition-colors hover:text-foreground"
                    onClick={() => navigateTo('')}
                  />
                }
              >
                s3://{bucket}
              </BreadcrumbLink>
            </BreadcrumbItem>
            {prefixSegments.map((seg, i) => {
              const segPath = prefixSegments.slice(0, i + 1).join('/') + '/';
              const isLast = i === prefixSegments.length - 1;
              return (
                <span key={segPath} className="contents">
                  <BreadcrumbSeparator />
                  <BreadcrumbItem>
                    {isLast ? (
                      <BreadcrumbPage>{seg}</BreadcrumbPage>
                    ) : (
                      <BreadcrumbLink
                        render={
                          <button
                            type="button"
                            className="transition-colors hover:text-foreground"
                            onClick={() => navigateTo(segPath)}
                          />
                        }
                      >
                        {seg}
                      </BreadcrumbLink>
                    )}
                  </BreadcrumbItem>
                </span>
              );
            })}
          </BreadcrumbList>
        </Breadcrumb>
      </div>

      {/* Search */}
      <InlineSearch
        value={filter}
        onChange={setFilter}
        placeholder="Filter by key..."
        className="max-w-sm"
      />

      {/* EMPTY-03 when bucket is genuinely empty (no prefix, no objects).
          Subdirectory emptiness stays as the inline table row since
          snippets are about bucket-level setup. */}
      {!objectsQ.isLoading &&
      !prefix &&
      (objectsQ.data?.items?.length ?? 0) === 0 ? (
        canUpload ? (
          <EmptyState
            icon={Terminal}
            title="No artifacts yet"
            description="Upload your first artifact using the snippet below."
          >
            <SnippetList
              repoType="s3"
              projectName={name}
              repoName={bucket}
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
      /* Object listing */
      <div className="overflow-hidden rounded-lg border">
        <table className="w-full text-sm">
          <thead className="bg-muted/50 text-xs uppercase text-muted-foreground">
            <tr>
              <th className="px-4 py-2 text-left">Key</th>
              <th className="px-4 py-2 text-right">Size</th>
              <th className="px-4 py-2 text-left">Last modified</th>
              <th className="px-4 py-2 text-left">ETag</th>
            </tr>
          </thead>
          <tbody>
            {objectsQ.isLoading ? (
              <tr>
                <td colSpan={4} className="px-4 py-6 text-center">
                  <Skeleton className="mx-auto h-5 w-48" />
                </td>
              </tr>
            ) : filtered.length === 0 ? (
              <tr>
                <td
                  colSpan={4}
                  className="px-4 py-6 text-center text-sm text-muted-foreground"
                >
                  No objects under this prefix.
                </td>
              </tr>
            ) : (
              filtered.map((r) => (
                <tr
                  key={r.key}
                  className="border-t transition-colors hover:bg-muted/30"
                >
                  <td className="px-4 py-2">
                    {r.isPrefix ? (
                      <button
                        type="button"
                        onClick={() => navigateTo(r.key)}
                        className="inline-flex items-center gap-1.5 text-primary hover:underline"
                      >
                        <Folder className="size-4 text-amber-500" />
                        {r.display}
                        <ChevronRight className="size-3 text-muted-foreground" />
                      </button>
                    ) : (
                      <span className="inline-flex items-center gap-1.5">
                        <FileIcon className="size-4 text-muted-foreground" />
                        {r.display}
                      </span>
                    )}
                  </td>
                  <td className="px-4 py-2 text-right tabular-nums">
                    {r.isPrefix ? '—' : formatBytes(r.size)}
                  </td>
                  <td className="px-4 py-2 text-muted-foreground">
                    {r.lastModified ? formatDate(r.lastModified) : '—'}
                  </td>
                  <td className="px-4 py-2 font-mono text-xs text-muted-foreground truncate max-w-[180px]">
                    {r.isPrefix ? '' : r.etag}
                  </td>
                </tr>
              ))
            )}
          </tbody>
        </table>
      </div>
      )}

      {objectsQ.data?.truncated && (
        <p className="text-xs text-muted-foreground">
          Showing the first 1,000 keys. Refine the prefix to see more.
        </p>
      )}

      {/* Delete bucket dialog */}
      <Dialog open={deleteOpen} onOpenChange={setDeleteOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete bucket &quot;{bucket}&quot;?</DialogTitle>
          </DialogHeader>
          <div className="space-y-4 py-4">
            <p className="text-sm text-muted-foreground">
              The bucket must be empty. Object deletes are synchronous
              (no trash); delete objects via the S3 SDK first if needed.
            </p>
            {(bucketQ.data?.object_count ?? 0) > 0 && (
              <div className="rounded-md bg-destructive/10 p-3 text-sm text-destructive">
                Bucket still has {bucketQ.data?.object_count} object
                {bucketQ.data?.object_count === 1 ? '' : 's'}. Delete will fail with 409.
              </div>
            )}
            {deleteError && (
              <ErrorEnvelopeRenderer envelope={deleteError} />
            )}
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setDeleteOpen(false)}>
              Cancel
            </Button>
            <Button
              variant="destructive"
              disabled={deleteBucket.isPending}
              onClick={onDeleteBucket}
            >
              {deleteBucket.isPending ? 'Deleting...' : 'Delete Bucket'}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
