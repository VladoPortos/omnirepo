/**
 * CreateRepoDialog — Phase 8 Plan 04 (MIRROR-16..21).
 *
 * Extracted from ProjectDetailPage's previously-inline Dialog so that:
 *   1. The MirrorConfigSection can be conditionally mounted here (only
 *      for protocol ∈ {deb,rpm,pypi,helm}) without bloating the page.
 *   2. Playwright can target a stable role=dialog with its own scoped
 *      test ID without fighting sibling dialogs (Delete project, S3
 *      bucket create etc. live in the same page).
 *   3. Future non-mirror creation concerns (quota, soft-limits) can
 *      plug into one place.
 *
 * API: the component owns all its form state. Consumers pass in the
 * project name + a callback invoked with the created repo (the caller
 * decides whether to navigate, toast, or switch tabs). Dialog close
 * and mutation errors are surfaced via the standard
 * ErrorEnvelopeRenderer.
 *
 * Wire contract:
 *   - POST /api/v1/projects/{name}/repos accepts mirror_* fields only
 *     when is_mirror === true (see
 *     internal/api/repos.go:handleCreateRepo five-branch validation).
 *     We strip mirror_* from the body when the opt-in is unchecked or
 *     the protocol is ineligible.
 *   - mirror_filter is sent as AnyFilter JSON with PascalCase keys
 *     matching the Go SyncFilter struct.
 */

import { useEffect, useState, type FormEvent } from 'react';
import { toast } from 'sonner';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select';
import { ErrorEnvelopeRenderer } from '@/components/common/ErrorEnvelope';
import { MirrorConfigSection } from '@/components/MirrorConfigSection';
import {
  envelopeFromError,
  fieldErrorsFromEnvelope,
  type ApiErrorEnvelope,
} from '@/api/client';
import { useCreateRepo } from '@/api/queries';
import type {
  MirrorConfigValue,
  Repo,
  RepoCreate,
  RepoType,
} from '@/api/types';

export interface CreateRepoDialogProps {
  open: boolean;
  onClose: () => void;
  projectName: string;
  /** Preselect the Select dropdown on open. Optional. */
  initialType?: RepoType;
  /** Fires after a successful create. Parent typically navigates or
   *  switches a tab. */
  onCreated?: (repo: Repo) => void;
}

// REPO_TYPES mirrors the labels in ProjectDetailPage. Kept here so the
// dialog stays independent; fine to re-duplicate four labels vs. carving
// out a shared module for one use site.
const REPO_TYPES: { value: RepoType; label: string }[] = [
  { value: 'docker', label: 'Docker' },
  { value: 'rpm', label: 'RPM' },
  { value: 'deb', label: 'APT' },
  { value: 'pypi', label: 'PyPI' },
  { value: 'helm', label: 'Helm' },
  { value: 'git', label: 'Git' },
  { value: 'raw', label: 'RAW' },
];

// Protocols that support the mirror flag (D-01 + D-02 + Phase 11 / D-13).
// Docker uses a different clone model (per-click modal, see
// CloneImageDialog); Raw has no upstream-index protocol to follow.
// Phase 11 / D-13 widens the set to include 'git' (HTTPS+PAT mirror via
// go-git/v6 PlainCloneContext + FetchContext, all-refs, see
// internal/protocol/git/sync_handler.go).
// eslint-disable-next-line prettier/prettier
const MIRROR_PROTOCOLS: ReadonlyArray<RepoType> = ['deb','rpm','pypi','helm','git'];

function isMirrorProtocol(
  t: RepoType,
): t is 'deb' | 'rpm' | 'pypi' | 'helm' | 'git' {
  return (MIRROR_PROTOCOLS as ReadonlyArray<string>).includes(t);
}

const EMPTY_MIRROR: MirrorConfigValue = {
  is_mirror: false,
  mirror_upstream_url: '',
  mirror_filter: {},
  mirror_cred_id: null,
  scan_on_sync: false,
};

export function CreateRepoDialog({
  open,
  onClose,
  projectName,
  initialType = 'docker',
  onCreated,
}: CreateRepoDialogProps) {
  const [repoName, setRepoName] = useState('');
  const [repoType, setRepoType] = useState<RepoType>(initialType);
  const [mirrorCfg, setMirrorCfg] = useState<MirrorConfigValue>(EMPTY_MIRROR);
  const [clientError, setClientError] = useState<string | null>(null);
  const [serverError, setServerError] = useState<ApiErrorEnvelope | null>(null);

  const createRepo = useCreateRepo();
  const fieldErrors = fieldErrorsFromEnvelope(serverError);

  // Reset state on open/close so re-opening never shows stale input.
  useEffect(() => {
    if (open) {
      setRepoName('');
      setRepoType(initialType);
      setMirrorCfg(EMPTY_MIRROR);
      setClientError(null);
      setServerError(null);
    }
  }, [open, initialType]);

  // When the protocol flips off the mirror-eligible set, force is_mirror
  // back to false so a stale toggle doesn't leak into the submit body.
  useEffect(() => {
    if (!isMirrorProtocol(repoType) && mirrorCfg.is_mirror) {
      setMirrorCfg(EMPTY_MIRROR);
    }
  }, [repoType, mirrorCfg.is_mirror]);

  const showMirrorSection = isMirrorProtocol(repoType);

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault();
    setClientError(null);
    setServerError(null);

    // Client-side mirror validation — display inline but do NOT trust;
    // backend re-validates and is authoritative.
    if (showMirrorSection && mirrorCfg.is_mirror) {
      const url = mirrorCfg.mirror_upstream_url.trim();
      if (url === '') {
        setClientError('Upstream URL is required');
        return;
      }
      if (!/^https?:\/\//i.test(url)) {
        setClientError('URL must use http(s)');
        return;
      }
    }

    const body: RepoCreate = {
      name: repoName.trim(),
      type: repoType,
    };
    if (showMirrorSection && mirrorCfg.is_mirror) {
      body.is_mirror = true;
      body.mirror_upstream_url = mirrorCfg.mirror_upstream_url.trim();
      body.mirror_filter = mirrorCfg.mirror_filter;
      body.mirror_cred_id = mirrorCfg.mirror_cred_id;
      body.scan_on_sync = mirrorCfg.scan_on_sync;
    }

    try {
      const created = await createRepo.mutateAsync({ projectName, data: body });
      toast.success(`Repository "${body.name}" created.`);
      onCreated?.(created);
      onClose();
    } catch (err) {
      setServerError(envelopeFromError(err, 'Failed to create repository.'));
    }
  };

  return (
    <Dialog open={open} onOpenChange={(v) => (!v ? onClose() : null)}>
      <DialogContent
        aria-label="Create Repository"
        className="flex max-h-[calc(100vh-4rem)] flex-col"
      >
        <form
          onSubmit={handleSubmit}
          className="flex min-h-0 flex-1 flex-col"
        >
          <DialogHeader>
            <DialogTitle>Create Repository</DialogTitle>
          </DialogHeader>
          <div className="flex-1 space-y-4 overflow-y-auto py-4">
            {serverError && <ErrorEnvelopeRenderer envelope={serverError} />}
            {clientError && (
              <p
                className="text-sm text-status-warning-foreground"
                role="alert"
                data-testid="create-repo-client-error"
              >
                {clientError}
              </p>
            )}

            <div className="space-y-2">
              <Label htmlFor="repo-type">Type</Label>
              <Select
                value={repoType}
                onValueChange={(v) => setRepoType(v as RepoType)}
              >
                <SelectTrigger id="repo-type">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {REPO_TYPES.map((rt) => (
                    <SelectItem key={rt.value} value={rt.value}>
                      {rt.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>

            <div className="space-y-2">
              <Label htmlFor="repo-name">Repository Name</Label>
              <Input
                id="repo-name"
                value={repoName}
                onChange={(e) => setRepoName(e.target.value)}
                placeholder="my-repo"
                required
                autoFocus
                aria-invalid={
                  !!fieldErrors['name'] ||
                  !!fieldErrors['repo-name'] ||
                  undefined
                }
              />
            </div>

            {showMirrorSection && (
              <MirrorConfigSection
                protocol={repoType as 'deb' | 'rpm' | 'pypi' | 'helm' | 'git'}
                projectName={projectName}
                value={mirrorCfg}
                onChange={setMirrorCfg}
              />
            )}
            {repoType === 'docker' && (
              <p className="rounded-md border border-muted bg-muted/40 p-3 text-xs text-muted-foreground">
                Docker repos do not support repo-level mirroring. To pull
                an image from an external registry into this repo, use{' '}
                <span className="font-medium">Pull external image</span>{' '}
                on the repo detail page after creating it.
              </p>
            )}
          </div>
          <DialogFooter>
            <Button variant="outline" type="button" onClick={onClose}>
              Cancel
            </Button>
            <Button
              type="submit"
              disabled={createRepo.isPending || !repoName.trim()}
            >
              {createRepo.isPending ? 'Creating...' : 'Create Repository'}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
