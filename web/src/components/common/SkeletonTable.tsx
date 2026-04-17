/**
 * SkeletonTable — table-shaped loading placeholder per Phase 6 UI-SPEC
 * (VISUAL-03). Outer container carries role="status" aria-label="Loading";
 * inner Skeleton cells are decorative.
 *
 * Props:
 *   rows    Number of body rows.
 *   columns Number of columns per row (header + body).
 *   widths? Optional per-column Tailwind width classes (e.g. ['w-24',
 *           'w-full', 'w-16']). If omitted — or shorter than `columns`
 *           — missing slots default to `w-full`.
 *
 * Note: widths is a string array so callers can pass arbitrary Tailwind
 * width utilities (`w-1/4`, `w-[18rem]`, etc.) without a type ceremony.
 */

import { Skeleton } from '@/components/ui/skeleton';

export interface SkeletonTableProps {
  rows: number;
  columns: number;
  widths?: string[];
}

export function SkeletonTable({
  rows,
  columns,
  widths,
}: SkeletonTableProps) {
  const colWidth = (i: number): string =>
    widths && widths[i] ? widths[i] : 'w-full';

  return (
    <div
      role="status"
      aria-label="Loading"
      className="rounded-lg border overflow-hidden"
    >
      <div className="border-b bg-muted/40 p-3">
        <div className="flex gap-4">
          {Array.from({ length: columns }).map((_, c) => (
            <Skeleton key={`h-${c}`} className={`h-3 ${colWidth(c)}`} />
          ))}
        </div>
      </div>
      <div className="divide-y">
        {Array.from({ length: rows }).map((_, r) => (
          <div key={`r-${r}`} className="flex gap-4 p-3">
            {Array.from({ length: columns }).map((_, c) => (
              <Skeleton
                key={`c-${r}-${c}`}
                className={`h-3 ${colWidth(c)}`}
              />
            ))}
          </div>
        ))}
      </div>
    </div>
  );
}
