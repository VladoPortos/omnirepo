/**
 * Commit detail view showing commit info and per-file diffs.
 */

import { ArrowLeft, GitCommitHorizontal, FileDiff, Plus, Minus } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Badge } from '@/components/ui/badge';
import { Skeleton } from '@/components/ui/skeleton';
import { DiffViewer } from './DiffViewer';
import { useGitCommitDetail } from '@/api/queries';
import { formatDate } from '@/lib/format';

interface CommitDetailProps {
  projectName: string;
  repoName: string;
  sha: string;
  onBack: () => void;
}

export function CommitDetail({
  projectName,
  repoName,
  sha,
  onBack,
}: CommitDetailProps) {
  const { data, isLoading } = useGitCommitDetail(projectName, repoName, sha);

  if (isLoading) {
    return (
      <div className="space-y-4">
        <Button variant="ghost" size="sm" onClick={onBack}>
          <ArrowLeft className="mr-1.5 size-4" />
          Back
        </Button>
        <Skeleton className="h-20 w-full" />
        <Skeleton className="h-64 w-full" />
      </div>
    );
  }

  if (!data) {
    return (
      <div className="space-y-4">
        <Button variant="ghost" size="sm" onClick={onBack}>
          <ArrowLeft className="mr-1.5 size-4" />
          Back
        </Button>
        <p className="text-center text-sm text-muted-foreground">
          Commit not found.
        </p>
      </div>
    );
  }

  return (
    <div className="space-y-4">
      <Button variant="ghost" size="sm" onClick={onBack}>
        <ArrowLeft className="mr-1.5 size-4" />
        Back to commits
      </Button>

      {/* Commit header */}
      <div className="rounded-lg border p-4">
        <div className="flex items-start gap-3">
          <GitCommitHorizontal className="mt-1 size-5 text-muted-foreground" />
          <div className="min-w-0 flex-1">
            <p className="text-sm font-semibold whitespace-pre-wrap">
              {data.message}
            </p>
            <p className="mt-1 text-xs text-muted-foreground">
              Committed {formatDate(data.stats ? '' : '')}
            </p>
            <div className="mt-2 flex flex-wrap items-center gap-2">
              <code className="rounded bg-muted px-2 py-0.5 font-mono text-xs">
                {data.sha.slice(0, 10)}
              </code>
              <Badge variant="outline" className="text-xs">
                <FileDiff className="mr-1 size-3" />
                {data.stats.files_changed} files
              </Badge>
              <Badge variant="outline" className="text-xs text-green-600">
                <Plus className="mr-0.5 size-3" />
                {data.stats.additions}
              </Badge>
              <Badge variant="outline" className="text-xs text-red-600">
                <Minus className="mr-0.5 size-3" />
                {data.stats.deletions}
              </Badge>
            </div>
          </div>
        </div>
      </div>

      {/* Per-file diffs */}
      <div className="space-y-4">
        {data.files.map((file) => {
          // Parse unified diff patch into old/new content
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
                  oldTitle={`a/${file.path}`}
                  newTitle={`b/${file.path}`}
                />
              </div>
            </div>
          );
        })}
      </div>
    </div>
  );
}

/**
 * Minimal unified diff parser: extracts old and new file content from a patch string.
 */
function parsePatch(patch: string): { oldContent: string; newContent: string } {
  if (!patch) return { oldContent: '', newContent: '' };

  const lines = patch.split('\n');
  const oldLines: string[] = [];
  const newLines: string[] = [];

  for (const line of lines) {
    if (line.startsWith('@@')) continue;
    if (line.startsWith('---')) continue;
    if (line.startsWith('+++')) continue;
    if (line.startsWith('-')) {
      oldLines.push(line.slice(1));
    } else if (line.startsWith('+')) {
      newLines.push(line.slice(1));
    } else if (line.startsWith(' ')) {
      oldLines.push(line.slice(1));
      newLines.push(line.slice(1));
    } else {
      // Context line without prefix
      oldLines.push(line);
      newLines.push(line);
    }
  }

  return {
    oldContent: oldLines.join('\n'),
    newContent: newLines.join('\n'),
  };
}
