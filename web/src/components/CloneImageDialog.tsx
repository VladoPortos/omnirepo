/**
 * CloneImageDialog — Phase 8 / plan 08-03 Docker clone-external modal.
 *
 * Three render states driven by a single `phase` state variable:
 *
 *   form      — operator types source reference + optional retag + optional
 *                cred + optional scan-override, clicks Pull to enqueue a
 *                pull-external job.
 *   progress  — mutation returned a job_id; hook polls sync-jobs/{id} every
 *                500 ms; bar + "Layer N of M · X MiB / Y MiB · P%" line.
 *   result    — progress.status flipped to done or failed; render success
 *                message or ErrorEnvelopeRenderer with Retry / Close.
 *
 * Backend contract (authoritative — DIFFERS from plan 08-03's sketch):
 *   - POST /api/v1/projects/{name}/repos/docker/{repo}/pull-external
 *     Body: { src_image, dst_tag?, cred_id?, src_username?, src_password? }
 *     (plan's `src` + `retag_as` + `scan_override` fields do NOT exist —
 *     see internal/protocol/oci/pull_external.go:PullExternalRequest).
 *     The repo's stored `auto_scan` flag governs per-pull scanning; a UI
 *     "scan override" checkbox is not yet wired server-side in v1.1, so
 *     we surface the repo's current auto_scan state as read-only
 *     information rather than a misleading no-op toggle.
 *   - GET  /api/v1/projects/{name}/repos/{type}/{repo}/sync-jobs/{id}
 *     (per-repo scope, NOT the global /api/v1/jobs/{id} the plan claims).
 *
 * Cancel button behaviour: v1.1 has no server-side job cancellation
 * (design-spec §Risks). The footer button in the progress phase closes
 * the modal locally; the background job continues and the result lands
 * in the image list on completion. Button label + title document this.
 */

'use client';
import { useEffect, useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { Download, Loader2, CheckCircle2 } from 'lucide-react';
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
  DialogFooter,
} from '@/components/ui/dialog';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { Progress } from '@/components/ui/progress';
import { ErrorEnvelopeRenderer } from '@/components/common/ErrorEnvelope';
import { envelopeFromError, type ApiErrorEnvelope } from '@/api/client';
import { useJobProgress } from '@/hooks/useJobProgress';
import { usePullExternal, useUpstreamCreds } from '@/api/queries';

type Phase = 'form' | 'progress' | 'result';

export interface CloneImageDialogProps {
  open: boolean;
  onClose: () => void;
  projectName: string;
  repoName: string;
  repoId: number;
}

/**
 * fmtBytes — small byte formatter used in the "X MiB / Y MiB" progress
 * line. Mirrors the spec render "42 MiB / 103 MiB". Helm charts use
 * total_bytes=0; callers should special-case by checking the raw number
 * before passing through here. This helper only formats a single number.
 */
function fmtBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return '0 B';
  if (bytes >= 1024 * 1024) return (bytes / (1024 * 1024)).toFixed(1) + ' MiB';
  if (bytes >= 1024) return (bytes / 1024).toFixed(0) + ' KiB';
  return `${bytes} B`;
}

export function CloneImageDialog({
  open,
  onClose,
  projectName,
  repoName,
  repoId,
}: CloneImageDialogProps) {
  const [phase, setPhase] = useState<Phase>('form');
  const [srcImage, setSrcImage] = useState('');
  const [dstTag, setDstTag] = useState('');
  const [credId, setCredId] = useState<string>('');
  const [jobId, setJobId] = useState<number | null>(null);
  const [mutationError, setMutationError] = useState<ApiErrorEnvelope | null>(
    null,
  );

  const qc = useQueryClient();
  const mutation = usePullExternal(projectName, repoName);
  const progress = useJobProgress(projectName, 'docker', repoName, jobId);
  const credsQ = useUpstreamCreds(projectName);

  // Reset form + phase whenever the dialog opens. Prevents stale values
  // from flashing on a reopen after a previous clone.
  useEffect(() => {
    if (open) {
      setPhase('form');
      setSrcImage('');
      setDstTag('');
      setCredId('');
      setJobId(null);
      setMutationError(null);
      mutation.reset();
    }
    // `mutation` is stable across renders (TanStack memoises); omitting
    // it from deps is intentional to avoid reset loops.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);

  // Advance to result phase when the polled job terminates.
  useEffect(() => {
    if (phase !== 'progress') return;
    if (progress.status === 'done') {
      setPhase('result');
      qc.invalidateQueries({
        queryKey: ['repo-content', projectName, 'docker', repoName],
      });
      qc.invalidateQueries({
        queryKey: ['repo-scans', projectName, 'docker', repoName],
      });
      qc.invalidateQueries({
        queryKey: ['projects', projectName, 'repos'],
      });
    } else if (progress.status === 'failed') {
      setPhase('result');
    }
    // repoId is tracked on the props for future per-repo caches (e.g. a
    // `['repo', repoId]` key in Phase 9). Touch it in effect deps so
    // switching repos mid-modal doesn't stall the invalidation.
  }, [phase, progress.status, projectName, repoName, repoId, qc]);

  const handleSubmit = () => {
    setMutationError(null);
    const body: {
      src_image: string;
      dst_tag?: string;
      cred_id?: number;
    } = { src_image: srcImage.trim() };
    const trimmedDst = dstTag.trim();
    if (trimmedDst) body.dst_tag = trimmedDst;
    if (credId) {
      const n = Number(credId);
      if (Number.isFinite(n) && n > 0) body.cred_id = n;
    }
    mutation.mutate(body, {
      onSuccess: (resp) => {
        setJobId(resp.job_id);
        setPhase('progress');
      },
      onError: (err) => {
        setMutationError(
          envelopeFromError(err, 'Failed to enqueue pull-external job.'),
        );
      },
    });
  };

  const handleRetry = () => {
    setPhase('form');
    setJobId(null);
    setMutationError(null);
    mutation.reset();
  };

  const handleClose = () => {
    onClose();
  };

  const progressPercent = progress.percent ?? 0;
  const progressLine = (() => {
    const step = progress.currentStep || 'Preparing…';
    if (progress.totalBytes > 0) {
      const frac = `${fmtBytes(progress.progressBytes)} / ${fmtBytes(progress.totalBytes)}`;
      const pct = progress.percent == null ? '?' : `${progress.percent}`;
      return `${step} · ${frac} · ${pct}%`;
    }
    // Helm step-based progress: total_bytes==0, step carries "chart N of M".
    if (progress.progressBytes > 0) {
      return `${step} · ${fmtBytes(progress.progressBytes)} transferred`;
    }
    return step;
  })();

  return (
    <Dialog open={open} onOpenChange={(o: boolean) => (!o ? onClose() : null)}>
      <DialogContent aria-label="Clone external image">
        <DialogHeader>
          <DialogTitle>Clone external image</DialogTitle>
          <DialogDescription>
            Pull an image from an external registry into{' '}
            <span className="font-mono">{repoName}</span>. The destination
            repository&rsquo;s auto-scan setting governs whether the image is
            scanned after pull.
          </DialogDescription>
        </DialogHeader>

        {phase === 'form' ? (
          <div className="space-y-3 py-2">
            <div className="space-y-1.5">
              <Label htmlFor="clone-src">Source reference</Label>
              <Input
                id="clone-src"
                placeholder="docker.io/library/nginx:1.27"
                value={srcImage}
                onChange={(e) => setSrcImage(e.target.value)}
                autoFocus
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="clone-dst">Retag as (optional)</Label>
              <Input
                id="clone-dst"
                placeholder="library/nginx:1.27"
                value={dstTag}
                onChange={(e) => setDstTag(e.target.value)}
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="clone-cred">Upstream credential (optional)</Label>
              <select
                id="clone-cred"
                value={credId}
                onChange={(e) => setCredId(e.target.value)}
                className="h-8 w-full rounded-lg border border-input bg-transparent px-2 text-sm outline-none focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50 disabled:opacity-50"
                disabled={credsQ.isLoading}
              >
                <option value="">(anonymous — public registry)</option>
                {(credsQ.data ?? []).map((c) => (
                  <option key={c.id} value={String(c.id)}>
                    {c.host}
                    {c.username ? ` (${c.username})` : ''}
                  </option>
                ))}
              </select>
            </div>
            {mutationError ? (
              <ErrorEnvelopeRenderer envelope={mutationError} mode="inline" />
            ) : null}
          </div>
        ) : null}

        {phase === 'progress' ? (
          <div className="space-y-3 py-2">
            <h3 className="text-sm font-semibold">
              Pulling <span className="font-mono">{srcImage}</span>&hellip;
            </h3>
            <p
              className="text-xs text-muted-foreground font-mono"
              data-testid="clone-progress-line"
            >
              {progressLine}
            </p>
            <Progress
              value={progressPercent}
              aria-label={`Clone progress — ${progress.currentStep || 'starting'}`}
              aria-valuenow={progressPercent}
              aria-valuemin={0}
              aria-valuemax={100}
            />
          </div>
        ) : null}

        {phase === 'result' && progress.status === 'done' ? (
          <div className="flex items-start gap-2 py-4">
            <CheckCircle2
              className="size-5 mt-0.5 text-status-healthy-foreground shrink-0"
              aria-hidden="true"
            />
            <div className="flex-1">
              <p className="text-sm font-semibold">
                Cloned <span className="font-mono">{srcImage}</span>{' '}
                successfully.
              </p>
              <p className="text-xs text-muted-foreground mt-1">
                The image now appears in this repository&rsquo;s tag list.
              </p>
            </div>
          </div>
        ) : null}

        {phase === 'result' && progress.status === 'failed' ? (
          <div className="py-2">
            <ErrorEnvelopeRenderer envelope={progress.error} mode="inline" />
          </div>
        ) : null}

        <DialogFooter>
          {phase === 'form' ? (
            <>
              <Button variant="outline" onClick={handleClose}>
                Cancel
              </Button>
              <Button
                onClick={handleSubmit}
                disabled={!srcImage.trim() || mutation.isPending}
              >
                {mutation.isPending ? (
                  <Loader2 className="mr-1.5 size-4 animate-spin" />
                ) : (
                  <Download className="mr-1.5 size-4" />
                )}
                Pull
              </Button>
            </>
          ) : null}
          {phase === 'progress' ? (
            <Button
              variant="outline"
              onClick={handleClose}
              title="Closes this dialog. The background pull continues; the image appears in the tag list when it finishes."
            >
              Close (pull continues in background)
            </Button>
          ) : null}
          {phase === 'result' ? (
            <>
              {progress.status === 'failed' ? (
                <Button variant="outline" onClick={handleRetry}>
                  Retry
                </Button>
              ) : null}
              <Button onClick={handleClose}>Close</Button>
            </>
          ) : null}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
