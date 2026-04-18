/**
 * EmptyState — shared empty-data surface for Phase 7 EMPTY-01..06, EMPTY-08.
 *
 * Props API is LOCKED per 07-CONTEXT.md §E-01 and 07-UI-SPEC.md §"Component
 * Inventory". Borderless, centered, muted-foreground. Callers wrap in
 * <Card> if they need panel chrome.
 *
 * a11y contract (E-08):
 * - role="status" + aria-label={title} on root (SR announces once)
 * - inner icon is aria-hidden="true" (decorative)
 * - data-testid="empty-state" for Playwright assertEmptyState helper
 *
 * CTA rendering notes:
 * - `to` → react-router <Link> composed with shadcn-style Button via
 *   the base-ui `render` prop + `nativeButton={false}` — this repo's
 *   Button wraps `@base-ui/react/button`, NOT shadcn/Radix, so the
 *   `asChild` prop does not exist. The `render=<Link />` pattern is
 *   the established precedent (see DashboardPage, ProjectDetailPage,
 *   NotFoundPage).
 * - `onClick` → imperative <Button onClick>.
 * - `disabled` → Tooltip-wrapped disabled Button. The Tooltip wrapper
 *   here is base-ui (`@base-ui/react/tooltip`), so TooltipTrigger uses
 *   the `render` prop — NEVER the shadcn/Radix `asChild` pattern.
 *   The disabled Button is wrapped in a `<span className="inline-block">`
 *   so pointer events surface (a bare `<Button disabled>` has
 *   `pointer-events-none` which would otherwise swallow hover).
 *
 * NO raw Tailwind named palette; NO StatusBadge (empty ≠ status).
 */

import type { LucideIcon } from 'lucide-react';
import type { ReactNode } from 'react';
import { Link } from 'react-router-dom';

import { Button } from '@/components/ui/button';
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip';
import { cn } from '@/lib/utils';

export interface EmptyStateCTA {
  label: string;
  /** react-router target (mutually exclusive with `onClick`). */
  to?: string;
  /** imperative handler (mutually exclusive with `to`). */
  onClick?: () => void;
  disabled?: boolean;
  /** Tooltip content when `disabled`; defaults to "Action unavailable". */
  disabledHint?: string;
}

export interface EmptyStateProps {
  icon?: LucideIcon;
  title: string;
  /** ReactNode (not string) so EMPTY-08 can embed example chips inline. */
  description?: ReactNode;
  primaryCTA?: EmptyStateCTA;
  /** EMPTY-03 inline SnippetList slot only — rendered between description and CTA. */
  children?: ReactNode;
  className?: string;
}

export function EmptyState({
  icon: Icon,
  title,
  description,
  primaryCTA,
  children,
  className,
}: EmptyStateProps) {
  if (import.meta.env.DEV && primaryCTA) {
    const hasTo = primaryCTA.to !== undefined;
    const hasOnClick = primaryCTA.onClick !== undefined;
    if (!primaryCTA.disabled && hasTo === hasOnClick) {
      // eslint-disable-next-line no-console
      console.warn(
        'EmptyState: primaryCTA must provide exactly one of `to` or `onClick` ' +
          '(or `disabled: true`).',
      );
    }
  }

  return (
    <div
      data-testid="empty-state"
      role="status"
      aria-label={title}
      className={cn(
        'flex flex-col items-center text-center py-12 px-6',
        className,
      )}
    >
      {Icon ? (
        <Icon aria-hidden="true" className="size-12 text-muted-foreground" />
      ) : null}
      <h2 className="mt-4 text-lg font-semibold">{title}</h2>
      {description ? (
        <div
          className={cn(
            'mt-2 max-w-md text-sm text-muted-foreground',
            children ? 'mb-4' : undefined,
          )}
        >
          {description}
        </div>
      ) : null}
      {children}
      {primaryCTA?.disabled ? (
        <Tooltip>
          <TooltipTrigger
            render={
              <span
                className="mt-6 inline-block rounded-md focus-visible:outline-hidden focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2"
                tabIndex={0}
                role="button"
                aria-disabled="true"
                aria-label={primaryCTA.label}
              >
                <Button disabled tabIndex={-1}>
                  {primaryCTA.label}
                </Button>
              </span>
            }
          />
          <TooltipContent>
            {primaryCTA.disabledHint ?? 'Action unavailable'}
          </TooltipContent>
        </Tooltip>
      ) : primaryCTA?.to ? (
        <Button
          className="mt-6"
          nativeButton={false}
          render={<Link to={primaryCTA.to}>{primaryCTA.label}</Link>}
        />
      ) : primaryCTA?.onClick ? (
        <Button className="mt-6" onClick={primaryCTA.onClick}>
          {primaryCTA.label}
        </Button>
      ) : null}
    </div>
  );
}
