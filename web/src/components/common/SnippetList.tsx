/**
 * SnippetList — per-protocol CLI-snippet body lifted from SnippetPanel.
 *
 * Rendered by both the SnippetPanel Sheet and the EMPTY-03 EmptyState
 * children slot so both surfaces render identical copy (Phase 7 E-03).
 *
 * Uses ONLY Phase-6-approved typography (text-sm font-semibold header,
 * font-mono text-xs body). 8px (right-2 top-2) copy-button inset per
 * UI-SPEC §Spacing — new files MUST NOT be added to the
 * lint-spacing-carveout exclude list.
 *
 * a11y: contextual aria-label per snippet (`Copy ${snippet.label}`)
 * surfaces through CopyButton's optional aria-label override.
 */

import { ScrollArea } from '@/components/ui/scroll-area';
import { CopyButton } from './CopyButton';
import { getSnippets } from '@/lib/snippets';
import type { RepoType } from '@/api/types';

export interface SnippetListProps {
  repoType: RepoType;
  projectName: string;
  repoName: string;
  hostname: string;
  className?: string;
  /**
   * Plan 11-10 / GITMIRROR-03 (D-09): hide push-related snippet blocks
   * when the repo is a mirror (read-only). The clone/pull sections
   * remain visible so the read path is still discoverable.
   *
   * Filter rules per repoType (label-based; matches getSnippets copy):
   *   - git:    drop "Authenticate" (the credential-helper block that
   *             is only useful for push/fetch against a writable repo).
   *             Keep "Clone".
   *   - helm:   drop "helm push (OCI)".
   *   - pypi:   drop ".pypirc" and "twine upload".
   *   - docker: drop "Push" and "Login".
   *   - raw:    drop "Upload".
   *   - deb/rpm/s3: no push snippet distinguished here; pass through.
   * Currently GitRepoPage is the only caller that sets this prop — the
   * other protocols have their own mirror-aware EmptyState path via
   * SyncNowButton — but the filter is defined generically so it's safe
   * to extend later without another prop.
   */
  hidePush?: boolean;
}

/** Labels (per lib/snippets.ts) that describe write/push actions. The
 * SnippetList filters these out when hidePush is true. Kept as a Set
 * for O(1) membership check and so the intent is testable without
 * greping the snippet strings at render time. */
const PUSH_LABELS = new Set<string>([
  // git — auth is only relevant for push/fetch against a writable remote.
  'Authenticate',
  // helm OCI push (traditional helm repo add doesn't support push).
  'helm push (OCI)',
  // pypi upload tooling.
  '.pypirc',
  'twine upload',
  // docker write path.
  'Login',
  'Push',
  // raw write path.
  'Upload',
]);

export function SnippetList({
  repoType,
  projectName,
  repoName,
  hostname,
  className,
  hidePush = false,
}: SnippetListProps) {
  const allSnippets = getSnippets(repoType, projectName, repoName, hostname);
  const snippets = hidePush
    ? allSnippets.filter((s) => !PUSH_LABELS.has(s.label))
    : allSnippets;
  return (
    <ScrollArea className={className}>
      <div className="space-y-4 pb-4">
        {snippets.map((snippet) => (
          <div key={snippet.label} className="space-y-1.5">
            <h4 className="text-sm font-semibold">{snippet.label}</h4>
            <div className="relative rounded-md bg-muted p-3 pr-10 font-mono text-xs">
              <pre className="overflow-x-auto whitespace-pre-wrap break-all">
                {snippet.cmd}
              </pre>
              <CopyButton
                text={snippet.cmd}
                className="absolute right-2 top-2"
                aria-label={`Copy ${snippet.label}`}
              />
            </div>
          </div>
        ))}
      </div>
    </ScrollArea>
  );
}
