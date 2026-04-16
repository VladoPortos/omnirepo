/**
 * Scrollable commit history for Git repositories.
 * Shows SHA, author, message, and relative date per commit.
 */

import { useState } from 'react';
import { GitCommitHorizontal, ChevronRight } from 'lucide-react';
import { Avatar, AvatarFallback } from '@/components/ui/avatar';
import { Button } from '@/components/ui/button';
import { Skeleton } from '@/components/ui/skeleton';
import { formatDate } from '@/lib/format';
import { useGitCommits } from '@/api/queries';
import type { GitCommit } from '@/api/types';

interface CommitLogProps {
  projectName: string;
  repoName: string;
  currentRef: string;
  onCommitClick: (sha: string) => void;
}

export function CommitLog({
  projectName,
  repoName,
  currentRef,
  onCommitClick,
}: CommitLogProps) {
  const [cursor, setCursor] = useState<string | undefined>(undefined);
  const { data, isLoading } = useGitCommits(
    projectName,
    repoName,
    currentRef,
    cursor,
  );

  const commits = data?.items ?? [];
  const hasMore = !!data?.next_cursor;

  if (isLoading && commits.length === 0) {
    return (
      <div className="space-y-3 py-4">
        {Array.from({ length: 5 }).map((_, i) => (
          <div key={i} className="flex items-center gap-3">
            <Skeleton className="size-8 rounded-full" />
            <div className="flex-1 space-y-1">
              <Skeleton className="h-4 w-3/4" />
              <Skeleton className="h-3 w-1/2" />
            </div>
          </div>
        ))}
      </div>
    );
  }

  if (commits.length === 0) {
    return (
      <div className="py-8 text-center text-sm text-muted-foreground">
        No commits found on this ref.
      </div>
    );
  }

  return (
    <div className="space-y-1 py-2">
      {commits.map((commit: GitCommit) => (
        <button
          key={commit.sha}
          className="flex w-full items-start gap-3 rounded-md px-3 py-2.5 text-left transition-colors hover:bg-muted/50"
          onClick={() => onCommitClick(commit.sha)}
        >
          <Avatar className="mt-0.5 size-7">
            <AvatarFallback className="text-xs">
              {commit.author_name
                .split(' ')
                .map((n) => n[0])
                .join('')
                .slice(0, 2)
                .toUpperCase()}
            </AvatarFallback>
          </Avatar>
          <div className="min-w-0 flex-1">
            <p className="truncate text-sm font-medium">
              {commit.message.split('\n')[0]}
            </p>
            <p className="text-xs text-muted-foreground">
              {commit.author_name} committed {formatDate(commit.author_date)}
            </p>
          </div>
          <div className="flex shrink-0 items-center gap-2">
            <code className="rounded bg-muted px-1.5 py-0.5 font-mono text-xs text-muted-foreground">
              <GitCommitHorizontal className="mr-1 inline size-3" />
              {commit.sha.slice(0, 7)}
            </code>
            <ChevronRight className="size-4 text-muted-foreground" />
          </div>
        </button>
      ))}
      {hasMore && (
        <div className="flex justify-center pt-2">
          <Button
            variant="outline"
            size="sm"
            onClick={() => setCursor(data?.next_cursor ?? undefined)}
            disabled={isLoading}
          >
            Load more commits
          </Button>
        </div>
      )}
    </div>
  );
}
