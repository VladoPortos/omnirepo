/**
 * Input field for per-repo inline filtering per D-06.
 * Client-side filtering on keypress (no debounce).
 */

import { Search } from 'lucide-react';
import { Input } from '@/components/ui/input';

interface InlineSearchProps {
  value: string;
  onChange: (value: string) => void;
  placeholder?: string;
  className?: string;
}

export function InlineSearch({
  value,
  onChange,
  placeholder = 'Filter by name...',
  className,
}: InlineSearchProps) {
  return (
    <div className={className}>
      <div className="relative">
        <Search className="absolute left-2.5 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
        <Input
          value={value}
          onChange={(e) => onChange(e.target.value)}
          placeholder={placeholder}
          className="pl-8"
        />
      </div>
    </div>
  );
}
