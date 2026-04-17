/**
 * StatusBadge — consistent status pill per Phase 6 UI-SPEC (VISUAL-01,
 * VISUAL-02).
 *
 * Uses ONLY the new --status-* CSS tokens via Tailwind utilities
 * (bg-status-*, text-status-*-foreground, border-status-*-border).
 * Raw Tailwind named-palette utilities are forbidden in new code per
 * UI-SPEC §Color Forbidden list — enforced by plan 08 lint gates.
 *
 * Six variants matched 1:1 to the CSS tokens shipped in index.css:
 * healthy / warning / failure / disabled / maintenance / neutral.
 * Two sizes: sm (pill-inside-table), md (card / page-level).
 * iconOnly mode renders only the lucide icon; the label prop becomes
 * the aria-label on the outer Badge so screen readers announce it.
 */

import {
  CheckCircle2,
  AlertTriangle,
  XCircle,
  MinusCircle,
  Wrench,
  Info,
  type LucideIcon,
} from 'lucide-react';
import { Badge } from '@/components/ui/badge';
import { cn } from '@/lib/utils';

export type StatusVariant =
  | 'healthy'
  | 'warning'
  | 'failure'
  | 'disabled'
  | 'maintenance'
  | 'neutral';

const statusStyles: Record<StatusVariant, string> = {
  healthy:
    'bg-status-healthy text-status-healthy-foreground border-status-healthy-border',
  warning:
    'bg-status-warning text-status-warning-foreground border-status-warning-border',
  failure:
    'bg-status-failure text-status-failure-foreground border-status-failure-border',
  disabled:
    'bg-status-disabled text-status-disabled-foreground border-status-disabled-border',
  maintenance:
    'bg-status-maintenance text-status-maintenance-foreground border-status-maintenance-border',
  neutral:
    'bg-status-neutral text-status-neutral-foreground border-status-neutral-border',
};

const statusIcons: Record<StatusVariant, LucideIcon> = {
  healthy: CheckCircle2,
  warning: AlertTriangle,
  failure: XCircle,
  disabled: MinusCircle,
  maintenance: Wrench,
  neutral: Info,
};

const sizeStyles = {
  sm: { badge: 'text-xs px-2 py-0.5 gap-1', icon: 'size-3' },
  md: { badge: 'text-xs px-2.5 py-1 gap-1', icon: 'size-3.5' },
} as const;

export interface StatusBadgeProps {
  status: StatusVariant;
  label: string;
  size?: 'sm' | 'md';
  iconOnly?: boolean;
  className?: string;
}

export function StatusBadge({
  status,
  label,
  size = 'md',
  iconOnly = false,
  className,
}: StatusBadgeProps) {
  const colorClass = statusStyles[status];
  const Icon = statusIcons[status];
  const sz = sizeStyles[size];

  if (iconOnly) {
    if (import.meta.env.DEV && !label) {
      // eslint-disable-next-line no-console
      console.warn(
        'StatusBadge: iconOnly requires a label for aria-label.',
      );
    }
    return (
      <Badge
        variant="outline"
        aria-label={label}
        className={cn(
          'inline-flex items-center',
          sz.badge,
          colorClass,
          className,
        )}
      >
        <Icon className={cn(sz.icon)} aria-hidden="true" />
      </Badge>
    );
  }

  return (
    <Badge
      variant="outline"
      className={cn(
        'inline-flex items-center',
        sz.badge,
        colorClass,
        className,
      )}
    >
      <Icon className={cn(sz.icon)} aria-hidden="true" />
      <span>{label}</span>
    </Badge>
  );
}
