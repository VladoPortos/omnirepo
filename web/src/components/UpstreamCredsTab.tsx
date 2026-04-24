/**
 * UpstreamCredsTab — Phase 8 Plan 05 (MIRROR-22..24).
 *
 * Project-scoped table of upstream credentials with Add / Edit / Delete
 * actions. Mounted inside ProjectSettingsPage under the "Upstream
 * credentials" tab.
 *
 * Security properties (T-08-05-01, T-08-05-07):
 *   - The UpstreamCred type omits password/token. The table renders
 *     host, kind, username, created — never any secret value.
 *   - On fetch error (backend 403 for non-members, 404 when AEAD is
 *     not materialised) we surface the envelope via
 *     ErrorEnvelopeRenderer — the backend is the security boundary;
 *     the UI is convenience only.
 *
 * Delete flow (T-08-05-08):
 *   - Second-click confirmation via inline AlertDialog-equivalent.
 *   - Confirmation copy explicitly warns that any mirror repo that
 *     references this credential will fail its next sync (the
 *     backend's ON DELETE SET NULL from plan 08-01 + clear
 *     "credential missing" envelope from plan 08-04).
 */

'use client';
import { useState } from 'react';
import { Key } from 'lucide-react';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table';
import { Button } from '@/components/ui/button';
import { Skeleton } from '@/components/ui/skeleton';
import { EmptyState } from '@/components/common/EmptyState';
import { ErrorEnvelopeRenderer } from '@/components/common/ErrorEnvelope';
import { envelopeFromError, type ApiErrorEnvelope } from '@/api/client';
import {
  useDeleteUpstreamCred,
  useUpstreamCreds,
} from '@/api/queries';
import { useRoleFor } from '@/hooks/useAuth';
import type { UpstreamCred } from '@/api/types';
import { UpstreamCredDialog } from './UpstreamCredDialog';

export interface UpstreamCredsTabProps {
  projectName: string;
}

export function UpstreamCredsTab({ projectName }: UpstreamCredsTabProps) {
  const q = useUpstreamCreds(projectName);
  const myRole = useRoleFor(projectName);
  const isMaintainer = myRole === 'maintainer';

  const [dialogOpen, setDialogOpen] = useState(false);
  const [editing, setEditing] = useState<UpstreamCred | undefined>(undefined);
  const [deleting, setDeleting] = useState<UpstreamCred | null>(null);
  const [deleteError, setDeleteError] = useState<ApiErrorEnvelope | null>(null);

  // The delete mutation is constructed per-target — credId is fixed at
  // construction time, so we recreate when `deleting` changes via key.
  const delMutation = useDeleteUpstreamCred(projectName, deleting?.id ?? 0);

  const openCreate = () => {
    setEditing(undefined);
    setDialogOpen(true);
  };
  const openEdit = (c: UpstreamCred) => {
    setEditing(c);
    setDialogOpen(true);
  };
  const closeDialog = () => setDialogOpen(false);

  const handleDeleteConfirm = () => {
    if (!deleting) return;
    setDeleteError(null);
    delMutation.mutate(undefined, {
      onSuccess: () => {
        setDeleting(null);
      },
      onError: (err) => {
        setDeleteError(
          envelopeFromError(err, 'Failed to delete credential.'),
        );
      },
    });
  };

  const handleCloseDelete = () => {
    setDeleting(null);
    setDeleteError(null);
    delMutation.reset();
  };

  if (q.isLoading) {
    return (
      <div className="space-y-2 py-4" data-testid="upstream-creds-loading">
        <Skeleton className="h-8 w-48" />
        <Skeleton className="h-10 w-full" />
        <Skeleton className="h-10 w-full" />
      </div>
    );
  }
  if (q.error) {
    const envelope = envelopeFromError(
      q.error,
      'Failed to load upstream credentials.',
    );
    return (
      <div className="py-4">
        <ErrorEnvelopeRenderer envelope={envelope} mode="page" />
      </div>
    );
  }
  const creds = q.data ?? [];

  return (
    <div className="space-y-4 py-2">
      <div className="flex items-center justify-between">
        <div>
          <h2 className="font-heading text-lg font-semibold">
            Upstream credentials
          </h2>
          <p className="text-xs text-muted-foreground">
            Stored per-project and never echoed back in API responses.
            Used by mirror syncs (APT / RPM / PyPI / Helm) and Docker
            clone-external.
          </p>
        </div>
        {creds.length > 0 && isMaintainer ? (
          <Button onClick={openCreate}>Add credential</Button>
        ) : null}
      </div>

      {creds.length === 0 ? (
        <EmptyState
          icon={Key}
          title="No upstream credentials"
          description="Add a credential to authenticate mirror syncs and Docker clones against private upstream archives."
          primaryCTA={
            isMaintainer
              ? { label: 'Add credential', onClick: openCreate }
              : { label: 'Add credential', disabled: true, disabledHint: 'Maintainer role required for this action.' }
          }
        />
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Host</TableHead>
              <TableHead>Kind</TableHead>
              <TableHead>Username</TableHead>
              <TableHead>Created</TableHead>
              <TableHead className="w-40 text-right">Actions</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {creds.map((c) => (
              <TableRow key={c.id} data-testid={`cred-row-${c.id}`}>
                <TableCell className="font-mono">{c.host}</TableCell>
                <TableCell>{c.kind}</TableCell>
                <TableCell>{c.username || '—'}</TableCell>
                <TableCell className="text-muted-foreground">
                  {new Date(c.created_at).toLocaleDateString()}
                </TableCell>
                <TableCell className="text-right">
                  {isMaintainer && (
                    <div className="inline-flex gap-1">
                      <Button
                        size="sm"
                        variant="outline"
                        onClick={() => openEdit(c)}
                      >
                        Edit
                      </Button>
                      <Button
                        size="sm"
                        variant="destructive"
                        onClick={() => {
                          setDeleteError(null);
                          delMutation.reset();
                          setDeleting(c);
                        }}
                      >
                        Delete
                      </Button>
                    </div>
                  )}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}

      {/* Add / Edit dialog — stays mounted while open. */}
      <UpstreamCredDialog
        open={dialogOpen}
        onClose={closeDialog}
        projectName={projectName}
        mode={editing ? 'edit' : 'create'}
        cred={editing}
      />

      {/* Delete-confirmation dialog. Inline AlertDialog-equivalent —
          the repo doesn't ship a shadcn AlertDialog primitive, so we
          compose the regular Dialog with destructive-copy + a
          destructive Confirm button. This is the second-click gate
          per T-08-05-08. */}
      <Dialog
        open={!!deleting}
        onOpenChange={(o: boolean) => (!o ? handleCloseDelete() : null)}
      >
        <DialogContent aria-label="Delete credential">
          <DialogHeader>
            <DialogTitle>
              {deleting ? (
                <>
                  Delete credential for{' '}
                  <span className="font-mono">{deleting.host}</span>?
                </>
              ) : (
                <>Delete credential?</>
              )}
            </DialogTitle>
            <DialogDescription>
              If any mirror repo references this credential, its next
              sync will fail with a clear &ldquo;credential missing&rdquo;
              envelope. The repo row itself stays intact and can be
              repointed from its Mirror config card.
            </DialogDescription>
          </DialogHeader>

          {deleteError ? (
            <ErrorEnvelopeRenderer envelope={deleteError} mode="inline" />
          ) : null}

          <DialogFooter>
            <Button
              variant="outline"
              onClick={handleCloseDelete}
              disabled={delMutation.isPending}
            >
              Cancel
            </Button>
            <Button
              variant="destructive"
              onClick={handleDeleteConfirm}
              disabled={delMutation.isPending || !deleting}
            >
              {delMutation.isPending ? 'Deleting…' : 'Delete'}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
