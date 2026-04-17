/**
 * Dev-only story page exercising the Phase 6 / plan 06 primitives:
 * StatusBadge (6 variants × 2 sizes + iconOnly), Skeleton* variants
 * (Card / Table / Detail / Metric), CopyInline (plain + masked),
 * and a simple light/dark theme toggle so visual verification covers
 * both :root and .dark token paths.
 *
 * Route registration in web/src/App.tsx is gated behind
 * DEV_ROUTES_ENABLED (import.meta.env.DEV OR VITE_OMNIREPO_DEV) so the
 * entire module is tree-shaken from production bundles.
 *
 * Playwright and humans drive this page to snapshot the primitives —
 * no page surface consumes them yet (plan 06-07 does that).
 */

import { useCallback, useState } from 'react';

import { StatusBadge, type StatusVariant } from '@/components/common/StatusBadge';
import { SkeletonCard } from '@/components/common/SkeletonCard';
import { SkeletonTable } from '@/components/common/SkeletonTable';
import { SkeletonDetail } from '@/components/common/SkeletonDetail';
import { SkeletonMetric } from '@/components/common/SkeletonMetric';
import { CopyInline } from '@/components/common/CopyInline';
import { Button } from '@/components/ui/button';

const STATUSES: Array<{ variant: StatusVariant; label: string }> = [
  { variant: 'healthy', label: 'Healthy' },
  { variant: 'warning', label: 'Stale' },
  { variant: 'failure', label: 'Failed' },
  { variant: 'disabled', label: 'Disabled' },
  { variant: 'maintenance', label: 'Maintenance' },
  { variant: 'neutral', label: 'Pending' },
];

export function PrimitivesStoryPage() {
  const [dark, setDark] = useState(false);

  const toggleTheme = useCallback(() => {
    setDark((prev) => {
      const next = !prev;
      document.documentElement.classList.toggle('dark', next);
      return next;
    });
  }, []);

  return (
    <div
      data-story="primitives"
      data-story-theme={dark ? 'dark' : 'light'}
      className="min-h-screen bg-background p-8 text-foreground"
    >
      <header className="mb-8 flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">
            Phase 6 primitives — story page
          </h1>
          <p className="text-sm text-muted-foreground">
            StatusBadge, Skeleton*, CopyInline. Dev-only surface; no
            production consumer yet.
          </p>
        </div>
        <Button
          variant="outline"
          size="sm"
          onClick={toggleTheme}
          data-story-theme-toggle
        >
          {dark ? 'Switch to light' : 'Switch to dark'}
        </Button>
      </header>

      <section data-story-section="status-md" className="mb-12 space-y-3">
        <h2 className="text-lg font-semibold">StatusBadge — size md</h2>
        <div className="flex flex-wrap gap-2">
          {STATUSES.map((s) => (
            <div
              key={`md-${s.variant}`}
              className="inline-flex"
              data-story-status={s.variant}
              data-story-size="md"
            >
              <StatusBadge status={s.variant} label={s.label} size="md" />
            </div>
          ))}
        </div>
      </section>

      <section data-story-section="status-sm" className="mb-12 space-y-3">
        <h2 className="text-lg font-semibold">StatusBadge — size sm</h2>
        <div className="flex flex-wrap gap-2">
          {STATUSES.map((s) => (
            <div
              key={`sm-${s.variant}`}
              className="inline-flex"
              data-story-status={s.variant}
              data-story-size="sm"
            >
              <StatusBadge status={s.variant} label={s.label} size="sm" />
            </div>
          ))}
        </div>
      </section>

      <section data-story-section="status-icon-only" className="mb-12 space-y-3">
        <h2 className="text-lg font-semibold">StatusBadge — iconOnly</h2>
        <div className="flex flex-wrap gap-2">
          {STATUSES.map((s) => (
            <div
              key={`icon-${s.variant}`}
              className="inline-flex"
              data-story-status={s.variant}
              data-story-size="md"
            >
              <StatusBadge status={s.variant} label={s.label} iconOnly />
            </div>
          ))}
        </div>
      </section>

      <section data-story-section="skeleton-card" className="mb-12 space-y-3">
        <h2 className="text-lg font-semibold">SkeletonCard</h2>
        <div className="grid gap-4 sm:grid-cols-2">
          <SkeletonCard />
          <SkeletonCard rows={5} showAction />
        </div>
      </section>

      <section data-story-section="skeleton-table" className="mb-12 space-y-3">
        <h2 className="text-lg font-semibold">SkeletonTable</h2>
        <SkeletonTable
          rows={4}
          columns={4}
          widths={['w-24', 'w-full', 'w-28', 'w-16']}
        />
      </section>

      <section data-story-section="skeleton-detail" className="mb-12 space-y-3">
        <h2 className="text-lg font-semibold">SkeletonDetail</h2>
        <div className="grid gap-8 md:grid-cols-2">
          <SkeletonDetail />
          <SkeletonDetail metaRows={6} showCode={false} />
        </div>
      </section>

      <section data-story-section="skeleton-metric" className="mb-12 space-y-3">
        <h2 className="text-lg font-semibold">SkeletonMetric</h2>
        <div className="grid gap-4 sm:grid-cols-2 md:grid-cols-4">
          <SkeletonMetric />
          <SkeletonMetric />
          <SkeletonMetric />
          <SkeletonMetric />
        </div>
      </section>

      <section data-story-section="copy-inline" className="mb-12 space-y-3">
        <h2 className="text-lg font-semibold">CopyInline</h2>
        <div className="max-w-xl space-y-3">
          <CopyInline
            value="https://omnirepo.example.com/v2/library/alpine"
            label="Registry URL"
          />
          <CopyInline
            value="sha256:2bc3f7a01e4e4b9d7f2e8a1c6d5f9b3e2a1c4d6e8f7b9a3c5d6e8f1a2b4c6d8e"
            label="Image digest"
          />
          <CopyInline
            value="omrepo_pat_supersecrettokenvalue1234567890abcdef"
            label="API key (masked)"
            masked
          />
        </div>
      </section>
    </div>
  );
}
