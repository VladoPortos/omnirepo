/**
 * Branch comparison view: select two refs and show commits + file diffs.
 */

import { useState } from 'react';
import { GitCompareArrows, FileDiff } from 'lucide-react';
import { Badge } from '@/components/ui/badge';
import { Skeleton } from '@/components/ui/skeleton';
import { RefSelector } from './RefSelector';
import { DiffViewer } from './DiffViewer';
import { useGitCompare } from '@/api/queries';
import { formatDate } from '@/lib/format';
import type { GitRef } from '@/api/types';

interface BranchCompareProps {
  projectName: string;
  repoName: string;
  refs: GitRef[];
  defaultBase?: string;
  defaultHead?: string;
}

export function BranchCompare({
  projectName,
  repoName,
  refs,
  defaultBase = '',
  defaultHead = '',
}: BranchCompareProps) {
  const [baseRef, setBaseRef] = useState(defaultBase);
  const [headRef, setHeadRef] = useState(defaultHead);

  const canCompare = baseRef && headRef && baseRef !== headRef;
  const { data, isLoading } = useGitCompare(
    projectName,
    repoName,
    canCompare ? baseRef : '',
    canCompare ? headRef : '',
  );

  return (
    <div className="space-y-4 py-4">
      {/* Ref selectors */}
      <div className="flex flex-wrap items-center gap-3">
        <div className="space-y-1">
          <label className="text-xs font-medium text-muted-foreground">Base</label>
          <RefSelector
            refs={refs}
            currentRef={baseRef}
            onRefChange={setBaseRef}
          />
        </div>
        <GitCompareArrows className="mt-4 size-5 text-muted-foreground" />
        <div className="space-y-1">
          <label className="text-xs font-medium text-muted-foreground">Compare</label>
          <RefSelector
            refs={refs}
            currentRef={headRef}
            onRefChange={setHeadRef}
          />
        </div>
      </div>

      {!canCompare && (
        <p className="text-sm text-muted-foreground">
          Select two different refs to compare.
        </p>
      )}

      {isLoading && canCompare && (
        <div className="space-y-3">
          <Skeleton className="h-16 w-full" />
          <Skeleton className="h-64 w-full" />
        </div>
      )}

      {data && (
        <div className="space-y-4">
          {/* Summary */}
          <div className="flex flex-wrap items-center gap-3 rounded-lg border p-3">
            <Badge variant="outline">
              {data.ahead_by} ahead
            </Badge>
            <Badge variant="outline">
              {data.behind_by} behind
            </Badge>
            <span className="text-sm text-muted-foreground">
              {data.commits.length} commits, {data.files.length} files changed
            </span>
          </div>

          {/* Commits */}
          {data.commits.length > 0 && (
            <div className="space-y-1 rounded-lg border p-3">
              <h3 className="mb-2 text-sm font-semibold">Commits</h3>
              {data.commits.map((commit) => (
                <div
                  key={commit.sha}
                  className="flex items-center gap-3 py-1.5 text-sm"
                >
                  <code className="shrink-0 font-mono text-xs text-muted-foreground">
                    {commit.sha.slice(0, 7)}
                  </code>
                  <span className="min-w-0 flex-1 truncate">
                    {commit.message.split('\n')[0]}
                  </span>
                  <span className="shrink-0 text-xs text-muted-foreground">
                    {formatDate(commit.author_date)}
                  </span>
                </div>
              ))}
            </div>
          )}

          {/* File diffs */}
          <div className="space-y-4">
            {data.files.map((file) => {
              const { oldContent, newContent } = parsePatch(file.patch);
              return (
                <div key={file.path} className="rounded-lg border">
                  <div className="flex items-center gap-2 border-b bg-muted/30 px-3 py-2">
                    <FileDiff className="size-4 text-muted-foreground" />
                    <span className="text-sm font-medium">{file.path}</span>
                    <Badge variant="outline" className="ml-auto text-xs">
                      {file.status}
                    </Badge>
                  </div>
                  <div className="overflow-x-auto">
                    <DiffViewer
                      oldValue={oldContent}
                      newValue={newContent}
                      oldTitle={`${baseRef}:${file.path}`}
                      newTitle={`${headRef}:${file.path}`}
                    />
                  </div>
                </div>
              );
            })}
          </div>
        </div>
      )}
    </div>
  );
}

function parsePatch(patch: string): { oldContent: string; newContent: string } {
  if (!patch) return { oldContent: '', newContent: '' };
  const lines = patch.split('\n');
  const oldLines: string[] = [];
  const newLines: string[] = [];
  for (const line of lines) {
    if (line.startsWith('@@') || line.startsWith('---') || line.startsWith('+++')) continue;
    if (line.startsWith('-')) {
      oldLines.push(line.slice(1));
    } else if (line.startsWith('+')) {
      newLines.push(line.slice(1));
    } else if (line.startsWith(' ')) {
      oldLines.push(line.slice(1));
      newLines.push(line.slice(1));
    } else {
      oldLines.push(line);
      newLines.push(line);
    }
  }
  return { oldContent: oldLines.join('\n'), newContent: newLines.join('\n') };
}
