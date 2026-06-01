/**
 * Badge with severity color per 05-UI-SPEC semantic colors.
 */

import type { ReactNode } from 'react';
import { cn } from '@/lib/utils';
import { Badge } from '@/components/ui/badge';

type SeverityLevel = 'critical' | 'high' | 'medium' | 'low' | 'unknown';

const severityStyles: Record<SeverityLevel, string> = {
  critical: 'bg-destructive/10 text-destructive border-destructive/20',
  high: 'bg-orange-500/10 text-orange-600 border-orange-500/20 dark:text-orange-400',
  medium: 'bg-amber-500/10 text-amber-600 border-amber-500/20 dark:text-amber-400',
  low: 'bg-teal-500/10 text-teal-600 border-teal-500/20 dark:text-teal-400',
  unknown: 'bg-muted text-muted-foreground border-border',
};

interface SeverityBadgeProps {
  severity: string;
  className?: string;
  /** Override the rendered label. Useful for count chips like "3". */
  children?: ReactNode;
}

export function SeverityBadge({ severity, className, children }: SeverityBadgeProps) {
  const level = severity.toLowerCase() as SeverityLevel;
  const style = severityStyles[level] ?? severityStyles.unknown;

  return (
    <Badge
      variant="outline"
      className={cn(style, className)}
    >
      {children ?? severity.charAt(0).toUpperCase() + severity.slice(1).toLowerCase()}
    </Badge>
  );
}
