/**
 * Branch/tag selector dropdown for Git repo browser.
 * Groups refs into Branches and Tags sections.
 */

import { GitBranch, Tag } from 'lucide-react';
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectLabel,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select';
import type { GitRef } from '@/api/types';

interface RefSelectorProps {
  refs: GitRef[];
  currentRef: string;
  onRefChange: (ref: string) => void;
  loading?: boolean;
}

export function RefSelector({
  refs,
  currentRef,
  onRefChange,
  loading,
}: RefSelectorProps) {
  const branches = refs.filter((r) => r.type === 'branch');
  const tags = refs.filter((r) => r.type === 'tag');

  return (
    <Select value={currentRef} onValueChange={(val) => { if (val) onRefChange(val); }} disabled={loading}>
      <SelectTrigger className="w-48">
        <SelectValue>
          {currentRef ? (
            <span className="flex items-center gap-1.5">
              <GitBranch className="size-3.5" />
              {currentRef}
            </span>
          ) : (
            'Select ref...'
          )}
        </SelectValue>
      </SelectTrigger>
      <SelectContent>
        {branches.length > 0 && (
          <SelectGroup>
            <SelectLabel>
              <span className="flex items-center gap-1.5">
                <GitBranch className="size-3.5" />
                Branches
              </span>
            </SelectLabel>
            {branches.map((b) => (
              <SelectItem key={`branch-${b.name}`} value={b.name}>
                {b.name}
              </SelectItem>
            ))}
          </SelectGroup>
        )}
        {tags.length > 0 && (
          <SelectGroup>
            <SelectLabel>
              <span className="flex items-center gap-1.5">
                <Tag className="size-3.5" />
                Tags
              </span>
            </SelectLabel>
            {tags.map((t) => (
              <SelectItem key={`tag-${t.name}`} value={t.name}>
                {t.name}
              </SelectItem>
            ))}
          </SelectGroup>
        )}
        {refs.length === 0 && !loading && (
          <div className="px-2 py-4 text-center text-sm text-muted-foreground">
            No refs found
          </div>
        )}
      </SelectContent>
    </Select>
  );
}
