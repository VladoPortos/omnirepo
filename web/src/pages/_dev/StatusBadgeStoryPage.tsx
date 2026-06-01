/**
 * Dev-only story page rendering a deterministic 24-variant matrix of
 * the StatusBadge primitive:
 *   6 statuses  (healthy, warning, failure, disabled, maintenance, neutral)
 *   × 2 sizes    (sm, md)
 *   × 2 iconOnly (false, true)
 *   = 24 rendered badges
 *
 * Each cell carries `data-story-variant`, `data-story-size`, and
 * `data-story-icon-only` attributes on a wrapping div so the
 * Playwright snapshot suite can locate every permutation
 * deterministically without widening StatusBadge's strict public props
 * interface.
 *
 * Route registration in web/src/App.tsx reuses the DEV_ROUTES_ENABLED
 * gate established by ErrorClassStoryPage and
 * PrimitivesStoryPage. Production builds tree-shake the
 * entire module — acceptance gate is `grep StatusBadgeStoryPage
 * web/dist/assets/*.js` returning zero matches.
 */

import { StatusBadge, type StatusVariant } from '@/components/common/StatusBadge';

const STATUSES: Array<{ variant: StatusVariant; label: string }> = [
  { variant: 'healthy', label: 'Healthy' },
  { variant: 'warning', label: 'Warning' },
  { variant: 'failure', label: 'Failure' },
  { variant: 'disabled', label: 'Disabled' },
  { variant: 'maintenance', label: 'Maintenance' },
  { variant: 'neutral', label: 'Neutral' },
];

const SIZES = ['sm', 'md'] as const;
const ICON_ONLY = [false, true] as const;

export function StatusBadgeStoryPage() {
  return (
    <div
      className="min-h-screen bg-background p-8 text-foreground space-y-8"
      data-story-root="status-badge"
    >
      <header className="space-y-1">
        <h1 className="text-2xl font-semibold tracking-tight">
          StatusBadge matrix
        </h1>
        <p className="text-xs text-muted-foreground">
          6 statuses × 2 sizes × 2 iconOnly values = 24 variants. The
          Playwright suite snapshots this page as a single
          visual-regression baseline. Dev-only — tree-shaken from
          production bundles.
        </p>
      </header>

      {ICON_ONLY.map((iconOnly) => (
        <section
          key={`io-${iconOnly}`}
          data-story-section={iconOnly ? 'icon-only' : 'labeled'}
          className="space-y-4"
        >
          <h2 className="text-lg font-semibold">
            {iconOnly ? 'iconOnly = true' : 'iconOnly = false'}
          </h2>

          {SIZES.map((size) => (
            <div key={`${size}-${iconOnly}`} className="space-y-2">
              <h3 className="text-sm text-muted-foreground">size = {size}</h3>
              <div
                className="flex flex-wrap items-center gap-3 rounded-lg border p-4"
                data-story-row={`${size}-${iconOnly ? 'icon' : 'labeled'}`}
              >
                {STATUSES.map((s) => (
                  <div
                    key={`${s.variant}-${size}-${iconOnly}`}
                    className="inline-flex"
                    data-story-variant={s.variant}
                    data-story-size={size}
                    data-story-icon-only={iconOnly ? 'true' : 'false'}
                  >
                    <StatusBadge
                      status={s.variant}
                      label={s.label}
                      size={size}
                      iconOnly={iconOnly}
                    />
                  </div>
                ))}
              </div>
            </div>
          ))}
        </section>
      ))}
    </div>
  );
}

export default StatusBadgeStoryPage;
