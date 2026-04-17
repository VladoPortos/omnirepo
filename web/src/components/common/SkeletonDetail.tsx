/**
 * SkeletonDetail — detail-panel loading placeholder per Phase 6 UI-SPEC
 * (VISUAL-03). Title Skeleton + N metadata rows (label / value pair per
 * row) + optional code-block Skeleton. Outer container carries
 * role="status" aria-label="Loading".
 *
 * Props:
 *   metaRows?  Number of label/value metadata rows (default 4).
 *   showCode?  Render a trailing code-block Skeleton (default true).
 */

import { Skeleton } from '@/components/ui/skeleton';

export interface SkeletonDetailProps {
  metaRows?: number;
  showCode?: boolean;
}

export function SkeletonDetail({
  metaRows = 4,
  showCode = true,
}: SkeletonDetailProps) {
  return (
    <div role="status" aria-label="Loading" className="space-y-6">
      <Skeleton className="h-6 w-64" />
      <div className="space-y-2">
        {Array.from({ length: metaRows }).map((_, i) => (
          <div key={i} className="flex gap-3">
            <Skeleton className="h-3 w-28" />
            <Skeleton className="h-3 flex-1" />
          </div>
        ))}
      </div>
      {showCode ? (
        <Skeleton className="h-24 w-full rounded-md" />
      ) : null}
    </div>
  );
}
