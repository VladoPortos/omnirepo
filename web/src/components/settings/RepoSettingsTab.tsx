/**
 * RepoSettingsTab — Phase 8 Plan 04 (MIRROR-16..21, D-18).
 *
 * Standalone repo-settings page mounted at
 *   /projects/:name/:type/:repo/settings
 *
 * Scope: the Mirror config card ONLY in this phase. Generic repo
 * settings (description, visibility, delete, etc.) are out of scope
 * and deferred to a later plan; this page is a first surface that
 * future edit controls can graft onto.
 *
 * Mirror config card behaviour:
 *   - Only rendered for repos with `is_mirror === true`.
 *   - Upstream URL is readonly (copy via CopyInline). The backend
 *     rejects any attempt to PATCH it with 400
 *     repo.mirror_url_immutable (plan 08-01 D-02).
 *   - Filter + cred + scan_on_sync are editable via MirrorConfigSection
 *     with `urlReadonly` + `hideCheckbox` props.
 *   - Save only sends the three editable fields — is_mirror and
 *     mirror_upstream_url are structurally excluded from the PATCH
 *     body to avoid the 400 immutable response.
 */

import { useEffect, useState } from 'react';
import { Link, useParams } from 'react-router-dom';
import { toast } from 'sonner';
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from '@/components/ui/card';
import { Button } from '@/components/ui/button';
import { Label } from '@/components/ui/label';
import { Skeleton } from '@/components/ui/skeleton';
import { CopyInline } from '@/components/common/CopyInline';
import { ErrorEnvelopeRenderer } from '@/components/common/ErrorEnvelope';
import { MirrorConfigSection } from '@/components/MirrorConfigSection';
import {
  envelopeFromError,
  type ApiErrorEnvelope,
} from '@/api/client';
import { useRepo, usePatchRepo } from '@/api/queries';
import type { MirrorConfigValue } from '@/api/types';

function deriveInitial(
  isMirror: boolean,
  upstreamURL: string,
  filterJSON: string,
  credID: number | null,
  scanOnSync: boolean,
): MirrorConfigValue {
  let filter: MirrorConfigValue['mirror_filter'] = {};
  if (filterJSON) {
    try {
      filter = JSON.parse(filterJSON);
    } catch {
      filter = {};
    }
  }
  return {
    is_mirror: isMirror,
    mirror_upstream_url: upstreamURL,
    mirror_filter: filter,
    mirror_cred_id: credID,
    scan_on_sync: scanOnSync,
  };
}

export function RepoSettingsTab() {
  const {
    name: projectParam = '',
    type: typeParam = '',
    repo: repoParam = '',
  } = useParams<{ name: string; type: string; repo: string }>();
  const projectName = decodeURIComponent(projectParam);
  const repoType = decodeURIComponent(typeParam);
  const repoName = decodeURIComponent(repoParam);

  const repoQ = useRepo(projectName, repoType, repoName);
  const patchMutation = usePatchRepo();

  const [localCfg, setLocalCfg] = useState<MirrorConfigValue | null>(null);
  const [initialCfg, setInitialCfg] = useState<MirrorConfigValue | null>(null);
  const [patchError, setPatchError] = useState<ApiErrorEnvelope | null>(null);

  // Hydrate local state when the repo query settles. Only re-sync when
  // the repo identity changes (id flip) so editing doesn't reset under
  // the user's fingers when TanStack re-fetches.
  useEffect(() => {
    if (!repoQ.data) return;
    const derived = deriveInitial(
      repoQ.data.is_mirror,
      repoQ.data.mirror_upstream_url,
      repoQ.data.mirror_filter_json,
      repoQ.data.mirror_cred_id,
      repoQ.data.scan_on_sync,
    );
    setInitialCfg(derived);
    setLocalCfg(derived);
  }, [repoQ.data?.id]); // eslint-disable-line react-hooks/exhaustive-deps

  if (repoQ.isLoading || !repoQ.data) {
    return (
      <div className="space-y-4">
        <Skeleton className="h-8 w-64" />
        <Skeleton className="h-48 w-full" />
      </div>
    );
  }

  const repo = repoQ.data;
  const dirty =
    initialCfg !== null &&
    localCfg !== null &&
    JSON.stringify(localCfg) !== JSON.stringify(initialCfg);

  const mirrorProtocol =
    repo.type === 'deb' ||
    repo.type === 'rpm' ||
    repo.type === 'pypi' ||
    repo.type === 'helm'
      ? (repo.type as 'deb' | 'rpm' | 'pypi' | 'helm')
      : null;

  const handleSave = async () => {
    if (!localCfg) return;
    setPatchError(null);
    // Explicitly construct the three editable fields — NEVER spread
    // localCfg so is_mirror + mirror_upstream_url can't smuggle through
    // even if the shape drifts (T-08-04-01 mitigation).
    try {
      await patchMutation.mutateAsync({
        projectName,
        repoType,
        repoName,
        data: {
          mirror_filter: localCfg.mirror_filter,
          mirror_cred_id: localCfg.mirror_cred_id,
          scan_on_sync: localCfg.scan_on_sync,
        },
      });
      toast.success('Mirror config saved.');
      setInitialCfg(localCfg); // flip dirty back to false
    } catch (err) {
      setPatchError(envelopeFromError(err, 'Failed to save mirror config.'));
    }
  };

  const handleCancel = () => {
    if (initialCfg) setLocalCfg(initialCfg);
  };

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-semibold">Repo settings — {repo.name}</h1>
        <p className="text-sm text-muted-foreground mt-1">
          <Link
            to={`/projects/${encodeURIComponent(projectName)}/${encodeURIComponent(repoType)}/${encodeURIComponent(repoName)}`}
            className="text-primary hover:underline"
          >
            Back to repo
          </Link>
        </p>
      </div>

      {repo.is_mirror && mirrorProtocol && localCfg ? (
        <Card>
          <CardHeader>
            <CardTitle>Mirror config</CardTitle>
          </CardHeader>
          <CardContent className="space-y-4">
            <div className="space-y-1.5">
              <Label>Upstream URL</Label>
              <CopyInline value={repo.mirror_upstream_url} />
              <p className="text-xs text-muted-foreground">
                URL is immutable — delete and recreate the repo to change.
              </p>
            </div>

            <MirrorConfigSection
              protocol={mirrorProtocol}
              projectName={projectName}
              value={localCfg}
              onChange={setLocalCfg}
              urlReadonly
              hideCheckbox
              hideUrl
              disabled={patchMutation.isPending}
            />

            {patchError && (
              <ErrorEnvelopeRenderer envelope={patchError} mode="inline" />
            )}

            <div className="flex justify-end gap-2">
              <Button
                variant="outline"
                onClick={handleCancel}
                disabled={!dirty || patchMutation.isPending}
              >
                Cancel
              </Button>
              <Button
                onClick={handleSave}
                disabled={!dirty || patchMutation.isPending}
              >
                {patchMutation.isPending ? 'Saving…' : 'Save'}
              </Button>
            </div>
          </CardContent>
        </Card>
      ) : (
        <Card>
          <CardContent className="py-8 text-sm text-muted-foreground">
            This repo is not a mirror. Mirror config only applies to
            repositories created with the "This repo is a mirror of an
            upstream" flag.
          </CardContent>
        </Card>
      )}
    </div>
  );
}
