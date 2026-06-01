/**
 * SkeletonMetric — metric-tile loading placeholder per Phase 6 UI-SPEC
 * (VISUAL-03). Mirrors the StorageGauge layout (label line + big number
 * + delta/footer line). Outer Card carries role="status"
 * aria-label="Loading".
 */

import { Skeleton } from '@/components/ui/skeleton';
import { Card, CardContent, CardHeader } from '@/components/ui/card';

export function SkeletonMetric() {
  return (
    <Card role="status" aria-label="Loading">
      <CardHeader className="pb-2">
        <Skeleton className="h-3 w-20" />
      </CardHeader>
      <CardContent className="space-y-2">
        <Skeleton className="h-6 w-24" />
        <Skeleton className="h-3 w-16" />
      </CardContent>
    </Card>
  );
}
