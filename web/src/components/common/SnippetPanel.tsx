/**
 * Sheet (slide-out panel) with protocol-aware CLI commands per D-16.
 *
 * The body — per-snippet label + <pre> + CopyButton — is rendered by the
 * shared <SnippetList /> primitive (Phase 7 E-03) so the Sheet and the
 * EMPTY-03 inline-snippet EmptyState surface stay in sync.
 */

import {
  Sheet,
  SheetTrigger,
  SheetContent,
  SheetHeader,
  SheetTitle,
  SheetDescription,
} from '@/components/ui/sheet';
import { Button } from '@/components/ui/button';
import { Terminal } from 'lucide-react';
import { SnippetList } from './SnippetList';
import type { RepoType } from '@/api/types';

interface SnippetPanelProps {
  repoType: RepoType;
  projectName: string;
  repoName: string;
  hostname: string;
  children?: React.ReactNode;
}

export function SnippetPanel({
  repoType,
  projectName,
  repoName,
  hostname,
  children,
}: SnippetPanelProps) {
  return (
    <Sheet>
      <SheetTrigger
        render={
          children ? (
            <>{children}</>
          ) : (
            <Button variant="outline" size="sm">
              <Terminal className="mr-1.5 size-4" />
              CLI Snippets
            </Button>
          )
        }
      />
      <SheetContent side="right">
        <SheetHeader>
          <SheetTitle>CLI Snippets</SheetTitle>
          <SheetDescription>
            Pre-filled commands for {repoType} repository{' '}
            <strong>
              {projectName}/{repoName}
            </strong>
          </SheetDescription>
        </SheetHeader>
        <SnippetList
          repoType={repoType}
          projectName={projectName}
          repoName={repoName}
          hostname={hostname}
          className="flex-1 px-4"
        />
      </SheetContent>
    </Sheet>
  );
}
