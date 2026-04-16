/**
 * Storage usage visualization using Progress component per D-03.
 * Shows used/total with percentage and accent-colored fill bar.
 */

import { Progress } from '@/components/ui/progress';
import { formatBytes } from '@/lib/format';
import { cn } from '@/lib/utils';

interface StorageGaugeProps {
  used: number;
  total: number;
  className?: string;
}

export function StorageGauge({ used, total, className }: StorageGaugeProps) {
  const percentage = total > 0 ? Math.min(Math.round((used / total) * 100), 100) : 0;

  return (
    <div className={cn('space-y-2', className)}>
      <div className="flex items-center justify-between text-sm">
        <span className="text-muted-foreground">Storage</span>
        <span className="font-medium tabular-nums">
          {formatBytes(used)} / {formatBytes(total)}
        </span>
      </div>
      <Progress value={percentage}>
        <span className="sr-only">{percentage}% used</span>
      </Progress>
      <p className="text-xs text-muted-foreground text-right tabular-nums">
        {percentage}% used
      </p>
    </div>
  );
}
