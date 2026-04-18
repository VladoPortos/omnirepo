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
}

export function SnippetList({
  repoType,
  projectName,
  repoName,
  hostname,
  className,
}: SnippetListProps) {
  const snippets = getSnippets(repoType, projectName, repoName, hostname);
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
