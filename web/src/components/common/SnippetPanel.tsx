/**
 * Sheet (slide-out panel) with protocol-aware CLI commands per D-16.
 * Each line has a CopyButton.
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
import { ScrollArea } from '@/components/ui/scroll-area';
import { Terminal } from 'lucide-react';
import { CopyButton } from './CopyButton';
import { getSnippets } from '@/lib/snippets';
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
  const snippets = getSnippets(repoType, projectName, repoName, hostname);

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
        <ScrollArea className="flex-1 px-4">
          <div className="space-y-4 pb-4">
            {snippets.map((snippet) => (
              <div key={snippet.label} className="space-y-1.5">
                <h4 className="text-sm font-medium">{snippet.label}</h4>
                <div className="relative rounded-md bg-muted p-3 pr-10 font-mono text-xs">
                  <pre className="overflow-x-auto whitespace-pre-wrap break-all">
                    {snippet.cmd}
                  </pre>
                  <CopyButton
                    text={snippet.cmd}
                    className="absolute right-1.5 top-1.5"
                  />
                </div>
              </div>
            ))}
          </div>
        </ScrollArea>
      </SheetContent>
    </Sheet>
  );
}
