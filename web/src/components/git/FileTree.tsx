/**
 * GitHub-style file tree browser for Git repositories.
 * Shows entries with folder/file icons, size, and last commit message.
 */

import { Folder, FileText } from 'lucide-react';
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table';
import { Skeleton } from '@/components/ui/skeleton';
import { formatBytes } from '@/lib/format';
import type { GitTreeEntry } from '@/api/types';

interface FileTreeProps {
  entries: GitTreeEntry[];
  loading?: boolean;
  currentPath: string;
  onNavigate: (entry: GitTreeEntry) => void;
  onBack?: () => void;
}

export function FileTree({
  entries,
  loading,
  currentPath,
  onNavigate,
  onBack,
}: FileTreeProps) {
  // Sort: folders first, then files, alphabetically within each group
  const sorted = [...entries].sort((a, b) => {
    if (a.type === 'tree' && b.type !== 'tree') return -1;
    if (a.type !== 'tree' && b.type === 'tree') return 1;
    return a.name.localeCompare(b.name);
  });

  if (loading) {
    return (
      <div className="space-y-2">
        {Array.from({ length: 6 }).map((_, i) => (
          <Skeleton key={i} className="h-8 w-full" />
        ))}
      </div>
    );
  }

  return (
    <Table>
      <TableHeader>
        <TableRow>
          <TableHead>Name</TableHead>
          <TableHead className="w-24 text-right">Size</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {currentPath && onBack && (
          <TableRow>
            <TableCell colSpan={2}>
              <button
                className="inline-flex items-center gap-2 text-sm text-primary hover:underline"
                onClick={onBack}
              >
                <Folder className="size-4 text-muted-foreground" />
                ..
              </button>
            </TableCell>
          </TableRow>
        )}
        {sorted.length === 0 && !currentPath && (
          <TableRow>
            <TableCell
              colSpan={2}
              className="h-24 text-center text-muted-foreground"
            >
              Empty repository
            </TableCell>
          </TableRow>
        )}
        {sorted.map((entry) => (
          <TableRow key={entry.path}>
            <TableCell>
              <button
                className="inline-flex items-center gap-2 text-sm text-primary hover:underline"
                onClick={() => onNavigate(entry)}
              >
                {entry.type === 'tree' ? (
                  <Folder className="size-4 text-blue-500" />
                ) : (
                  <FileText className="size-4 text-muted-foreground" />
                )}
                {entry.name}
              </button>
            </TableCell>
            <TableCell className="text-right text-xs text-muted-foreground">
              {entry.type === 'blob' ? formatBytes(entry.size) : '--'}
            </TableCell>
          </TableRow>
        ))}
      </TableBody>
    </Table>
  );
}
