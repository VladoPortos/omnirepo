/**
 * SkeletonCard — card-shaped loading placeholder per Phase 6 UI-SPEC
 * (VISUAL-03). Outer Card carries role="status" aria-label="Loading"
 * so screen readers announce the surface once; inner Skeleton bars are
 * decorative divs and do not announce individually.
 *
 * Props:
 *   rows?       Number of body Skeleton bars (default 3).
 *   showAction? Render an action-row Skeleton bar below body (default false).
 */

import { Skeleton } from '@/components/ui/skeleton';
import { Card, CardContent, CardHeader } from '@/components/ui/card';

export interface SkeletonCardProps {
  rows?: number;
  showAction?: boolean;
}

export function SkeletonCard({
  rows = 3,
  showAction = false,
}: SkeletonCardProps) {
  return (
    <Card role="status" aria-label="Loading">
      <CardHeader>
        <Skeleton className="h-4 w-32" />
      </CardHeader>
      <CardContent className="space-y-2">
        {Array.from({ length: rows }).map((_, i) => (
          <Skeleton key={i} className="h-3 w-full" />
        ))}
        {showAction ? <Skeleton className="h-8 w-24 mt-4" /> : null}
      </CardContent>
    </Card>
  );
}
