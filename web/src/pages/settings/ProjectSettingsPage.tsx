/**
 * ProjectSettingsPage — Phase 8 Plan 05 (MIRROR-22..24, D-19).
 *
 * New page mounted at /projects/:name/settings. Hosts project-scope
 * settings as a Tabs surface; plan 08-05 ships just the Upstream
 * credentials tab. Other project settings (members, general) are
 * out of scope for this plan — they continue to live on
 * ProjectDetailPage until a future rework moves them here.
 *
 * URL-param decoding follows the STATE.md [06-08] deviation:
 * `useParams()` returns the raw route match, which is the URL-encoded
 * slug; we decodeURIComponent before using it as a project name.
 *
 * Security: the underlying list fetch is project-member-gated on the
 * backend (auth.ActionManageUpstreamCreds via
 * resolveProjectAndCheckMembership in
 * internal/api/upstream_creds.go). Non-members see a permission
 * envelope via ErrorEnvelopeRenderer. Hiding the tab entirely is
 * polish — backend 403 is the security boundary.
 */

'use client';
import { Link, useParams } from 'react-router-dom';
import { ChevronLeft } from 'lucide-react';
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs';
import { UpstreamCredsTab } from '@/components/UpstreamCredsTab';

export function ProjectSettingsPage() {
  const { name = '' } = useParams();
  const projectName = decodeURIComponent(name);

  return (
    <div className="space-y-4">
      <div>
        <Link
          to={`/projects/${encodeURIComponent(projectName)}`}
          className="inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground"
        >
          <ChevronLeft className="size-3" />
          Back to project
        </Link>
        <h1 className="mt-1 font-heading text-lg font-semibold">
          Project settings — <span className="font-mono">{projectName}</span>
        </h1>
        <p className="text-sm text-muted-foreground">
          Per-project configuration. Changes take effect immediately.
        </p>
      </div>

      <Tabs defaultValue="upstream-creds">
        <TabsList variant="line" className="w-full justify-start pb-1.5">
          <TabsTrigger value="upstream-creds" className="flex-none px-3">
            Upstream credentials
          </TabsTrigger>
        </TabsList>
        <TabsContent value="upstream-creds">
          <UpstreamCredsTab projectName={projectName} />
        </TabsContent>
      </Tabs>
    </div>
  );
}
