/**
 * Dashboard page per D-03 + Phase 7 D-01..D-05.
 * Row 1: Projects, Repositories, Users, Scan Findings (4 equal-height cards).
 * Row 2: Phase 7 Composition row — 3 user-visible cards (Storage / Recent
 *        Failures / Scan Findings Trend) + 3 admin-only cards (Background
 *        Jobs / TLS Certificate / Trivy Database) gated on is_super_admin.
 *        Each card renders a StatusBadge derived from dashboard-thresholds.ts
 *        pure functions; cold-load uses SkeletonCard; fetch errors surface
 *        via ErrorEnvelopeRenderer.
 * Row 3: Full-width storage breakdown with progress bar + per-repo list.
 * Row 4: Recent Activity + High-Severity Findings (with CVE details).
 *
 * Phase 7 / D-05 string migrations:
 *   - :280 ("No recent activity.") → <EmptyState icon={Activity} ...>
 *   - :303 (positive zero-CVEs sentence) → inline <StatusBadge status="healthy" label="All clear" />
 *   - :402 ("No repositories with stored data.") → <EmptyState icon={HardDrive} ...>
 */

import { Link } from 'react-router-dom';
import { motion } from 'framer-motion';
import {
  FolderKanban,
  FolderGit2,
  Users,
  ShieldAlert,
  Plus,
  Activity,
  HardDrive,
} from 'lucide-react';
import { Card, CardContent, CardHeader, CardTitle, CardAction } from '@/components/ui/card';
import { Button } from '@/components/ui/button';
import { Skeleton } from '@/components/ui/skeleton';
import { Progress } from '@/components/ui/progress';
import { SeverityBadge } from '@/components/common/SeverityBadge';
import { SkeletonCard } from '@/components/common/SkeletonCard';
import { SkeletonMetric } from '@/components/common/SkeletonMetric';
import { StatusBadge, type StatusVariant } from '@/components/common/StatusBadge';
import { EmptyState } from '@/components/common/EmptyState';
import { ErrorEnvelopeRenderer } from '@/components/common/ErrorEnvelope';
import {
  useDashboard,
  useDashboardStorage,
  useMe,
  useAdminJobsSummary,
  useAdminTLSCurrent,
  useAdminTrivyDBStatus,
} from '@/api/queries';
import { envelopeFromError } from '@/api/client';
import {
  storageVariant,
  failuresVariant,
  scanFindingsVariant,
  jobsVariant,
  tlsVariant,
  trivyDBVariant,
} from '@/lib/dashboard-thresholds';
import { formatBytes, formatDate } from '@/lib/format';
import type {
  StorageRepoRow,
  DashboardVulnRow,
  DashboardActivityItem,
} from '@/api/types';

const cardVariants = {
  hidden: { opacity: 0, y: 12 },
  visible: (i: number) => ({
    opacity: 1,
    y: 0,
    transition: { delay: i * 0.05, duration: 0.2, ease: 'easeOut' as const },
  }),
};

const repoTypeColor: Record<string, { dot: string; bar: string }> = {
  docker: { dot: 'bg-blue-500', bar: '#3b82f6' },
  rpm: { dot: 'bg-red-500', bar: '#ef4444' },
  deb: { dot: 'bg-green-500', bar: '#22c55e' },
  pypi: { dot: 'bg-yellow-500', bar: '#eab308' },
  helm: { dot: 'bg-purple-500', bar: '#a855f7' },
  git: { dot: 'bg-orange-500', bar: '#f97316' },
  raw: { dot: 'bg-slate-500', bar: '#64748b' },
  s3: { dot: 'bg-cyan-500', bar: '#06b6d4' },
};
const defaultColor = { dot: 'bg-gray-400', bar: '#9ca3af' };

// storageStatusLabel — maps the storage threshold variant to its user-
// facing label per UI-SPEC §C-1 copy matrix. Kept as a pure function so
// the label and the StatusBadge share one source of truth.
function storageStatusLabel(v: StatusVariant): string {
  switch (v) {
    case 'healthy':
      return 'Healthy';
    case 'warning':
      return 'Filling up';
    case 'failure':
      return 'Nearly full';
    case 'disabled':
      return 'Not configured';
    default:
      return 'Unknown';
  }
}

// failuresStatusLabel — UI-SPEC §C-2 copy matrix.
function failuresStatusLabel(v: StatusVariant): string {
  switch (v) {
    case 'healthy':
      return 'All clear';
    case 'warning':
      return 'Some failures';
    case 'failure':
      return 'Many failures';
    default:
      return 'Unknown';
  }
}

// scanFindingsStatusLabel — UI-SPEC §C-3 copy matrix.
function scanFindingsStatusLabel(v: StatusVariant): string {
  switch (v) {
    case 'healthy':
      return 'All clear';
    case 'warning':
      return 'Action needed';
    case 'failure':
      return 'Critical findings';
    case 'disabled':
      return 'No scans yet';
    default:
      return 'Unknown';
  }
}

// jobsStatusLabel — UI-SPEC §C-4 copy matrix. jobsVariant only returns
// healthy/warning/failure (D-02 locks the variant set); the "maintenance"
// running-visual is overlaid here based on the raw counts.
function jobsStatusLabel(
  v: StatusVariant,
  running: number,
  queued: number,
  failedLast24h: number,
  lastCompletedAt: string | null,
): string {
  if (v === 'failure') return 'Jobs failed';
  // Still running/queued — surface "Running" over the generic healthy label.
  if (running > 0 || queued > 0) return 'Running';
  if (v === 'warning') return 'Some failed';
  // healthy + nothing ever run → "No jobs yet".
  if (failedLast24h === 0 && lastCompletedAt === null) return 'No jobs yet';
  return 'Idle';
}

// tlsStatusLabel — UI-SPEC §C-5 copy matrix.
function tlsStatusLabel(v: StatusVariant): string {
  switch (v) {
    case 'healthy':
      return 'Valid';
    case 'warning':
      return 'Expiring soon';
    case 'failure':
      return 'Expires urgently';
    case 'disabled':
      return 'Self-signed';
    default:
      return 'Unknown';
  }
}

// trivyDBStatusLabel — UI-SPEC §C-6 copy matrix.
function trivyDBStatusLabel(v: StatusVariant): string {
  switch (v) {
    case 'healthy':
      return 'Fresh';
    case 'warning':
      return 'Stale';
    case 'failure':
      return 'Outdated';
    case 'disabled':
      return 'Not initialised';
    default:
      return 'Unknown';
  }
}

// countRecentFailures — derive the Recent Failures C-2 count from the
// existing dashboard.recent_activity[] field. The server already scopes
// to the actor's visible projects (visibleProjectIDs in dashboard.go),
// so no additional filtering is needed here. An event counts as a
// "failure" when its action ends in `.failed` OR contains `error`.
function countRecentFailures(events: DashboardActivityItem[] | undefined): {
  count: number;
  latest: DashboardActivityItem[];
} {
  if (!events) return { count: 0, latest: [] };
  const cutoff = Date.now() - 24 * 60 * 60 * 1000;
  const matching = events.filter((e) => {
    const ts = new Date(e.created_at).getTime();
    if (Number.isNaN(ts) || ts < cutoff) return false;
    const action = e.action.toLowerCase();
    return action.endsWith('.failed') || action.includes('error');
  });
  return { count: matching.length, latest: matching.slice(0, 3) };
}

// daysUntil — return integer days between `now` and an RFC3339 string.
// Negative values indicate the date is already in the past.
function daysUntil(rfc3339: string | null | undefined): number {
  if (!rfc3339) return 0;
  const target = new Date(rfc3339).getTime();
  if (Number.isNaN(target)) return 0;
  return Math.floor((target - Date.now()) / (24 * 60 * 60 * 1000));
}

export function DashboardPage() {
  const { data, isLoading, isError: dashIsError, error: dashError, refetch: dashRefetch } = useDashboard();
  const { data: storageData, isLoading: storageLoading, isError: storageIsError, error: storageError, refetch: storageRefetch } = useDashboardStorage();

  // Phase 7 — admin-only card hooks. The `enabled` gate prevents non-
  // super-admin users from issuing a 403-generating request (server
  // enforces RequireCan gates regardless).
  const meQ = useMe();
  const isSuperAdmin = !!meQ.data?.is_super_admin;
  const jobsQ = useAdminJobsSummary(isSuperAdmin);
  const tlsQ = useAdminTLSCurrent(isSuperAdmin);
  const trivyQ = useAdminTrivyDBStatus(isSuperAdmin);

  const findings = data?.scan_findings;
  const totalFindings =
    (findings?.critical ?? 0) +
    (findings?.high ?? 0) +
    (findings?.medium ?? 0) +
    (findings?.low ?? 0);

  // Derived composition-card signals (undefined-safe; each card also
  // guards on isLoading / isError independently).
  const failuresData = countRecentFailures(data?.recent_activity);
  // We have no first-class "never scanned" flag — treat the absence of
  // any scan_findings object as "no scans yet". When we have a findings
  // object with all zeros, it means scans ran and found nothing.
  const neverScanned = !findings;
  const criticalCount = findings?.critical ?? 0;

  // Full-page loading state uses the Phase 6 canonical Skeleton* primitives
  // (VISUAL-03). Once any dashboard slice resolves, we fall through to the
  // real layout below where per-slice micro-skeletons handle independently
  // loading tiles (storage, activity, severity lists, composition cards).
  if (isLoading && storageLoading) {
    return (
      <div className="space-y-6">
        <div className="flex items-center justify-between">
          <h1 className="text-[28px] font-semibold leading-tight">Dashboard</h1>
        </div>

        {/* Row 1: 3 metric tiles + 1 findings tile */}
        <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-4">
          <SkeletonMetric />
          <SkeletonMetric />
          <SkeletonMetric />
          <SkeletonCard rows={2} />
        </div>

        {/* Row 2: Phase 7 Composition row — 6 skeleton slots. The admin-
            only slots render unconditionally during cold load; once data
            resolves, the real row conditionally renders them only when
            is_super_admin. This intentionally over-counts skeletons at
            cold-load rather than wait on `useMe()` to resolve before
            showing any composition skeleton — keeping perceived TTFP
            short matters more than exact slot count pre-hydration. */}
        <div className="grid gap-4 xl:gap-6 grid-cols-1 md:grid-cols-2 xl:grid-cols-3">
          <SkeletonCard rows={2} />
          <SkeletonCard rows={3} />
          <SkeletonCard rows={3} />
          <SkeletonCard rows={3} />
          <SkeletonCard rows={2} />
          <SkeletonCard rows={2} />
        </div>

        {/* Row 3: Storage breakdown */}
        <SkeletonCard rows={4} />

        {/* Row 4: Activity + severity */}
        <div className="grid gap-6 lg:grid-cols-2">
          <SkeletonCard rows={5} />
          <SkeletonCard rows={3} />
        </div>
      </div>
    );
  }

  return (
    <div className="space-y-6">
      {/* Header */}
      <div className="flex items-center justify-between">
        <h1 className="text-[28px] font-semibold leading-tight">Dashboard</h1>
        <div className="flex gap-2">
          <Button variant="outline" size="sm" nativeButton={false} render={<Link to="/projects?create=1" />}>
            <Plus className="mr-1.5 size-4" />
            Create Project
          </Button>
        </div>
      </div>

      {/* Row 1: Projects, Repositories, Users, Scan Findings — uniform height */}
      <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-4">
        {[
          {
            title: 'Projects',
            icon: FolderKanban,
            value: data?.project_count ?? 0,
          },
          {
            title: 'Repositories',
            icon: FolderGit2,
            value: data?.repo_count ?? 0,
          },
          {
            title: 'Users',
            icon: Users,
            value: data?.user_count ?? 0,
          },
        ].map((card, i) => (
          <motion.div
            key={card.title}
            custom={i}
            initial="hidden"
            animate="visible"
            variants={cardVariants}
          >
            {isLoading ? (
              <SkeletonMetric />
            ) : (
              <Card className="h-full">
                <CardHeader>
                  <div className="flex items-center justify-between">
                    <CardTitle className="text-sm font-medium text-muted-foreground">
                      {card.title}
                    </CardTitle>
                    <card.icon className="size-4 text-muted-foreground" />
                  </div>
                </CardHeader>
                <CardContent>
                  <p className="text-3xl font-bold tabular-nums">{card.value}</p>
                </CardContent>
              </Card>
            )}
          </motion.div>
        ))}

        {/* Scan Findings card — same height via h-full */}
        <motion.div
          custom={3}
          initial="hidden"
          animate="visible"
          variants={cardVariants}
        >
          {isLoading ? (
            <SkeletonCard rows={2} />
          ) : (
            <Card className="h-full">
              <CardHeader>
                <div className="flex items-center justify-between">
                  <CardTitle className="text-sm font-medium text-muted-foreground">
                    Scan Findings
                  </CardTitle>
                  <ShieldAlert className="size-4 text-muted-foreground" />
                </div>
              </CardHeader>
              <CardContent>
                <p className="text-3xl font-bold tabular-nums">{totalFindings}</p>
                {totalFindings > 0 && (
                  <div className="mt-2 flex flex-wrap gap-x-3 gap-y-1">
                    {(findings?.critical ?? 0) > 0 && (
                      <span className="flex items-center gap-1 text-sm">
                        <SeverityBadge severity="critical" />
                        <span className="tabular-nums">{findings!.critical}</span>
                      </span>
                    )}
                    {(findings?.high ?? 0) > 0 && (
                      <span className="flex items-center gap-1 text-sm">
                        <SeverityBadge severity="high" />
                        <span className="tabular-nums">{findings!.high}</span>
                      </span>
                    )}
                    {(findings?.medium ?? 0) > 0 && (
                      <span className="flex items-center gap-1 text-sm">
                        <SeverityBadge severity="medium" />
                        <span className="tabular-nums">{findings!.medium}</span>
                      </span>
                    )}
                    {(findings?.low ?? 0) > 0 && (
                      <span className="flex items-center gap-1 text-sm">
                        <SeverityBadge severity="low" />
                        <span className="tabular-nums">{findings!.low}</span>
                      </span>
                    )}
                  </div>
                )}
              </CardContent>
            </Card>
          )}
        </motion.div>
      </div>

      {/* Row 2: Phase 7 Composition row (D-01). Responsive grid: 1 col
          at sm, 2 col at md (covers 1366×768 baseline), 3 col at xl.
          StatusBadge variants ALL derive from dashboard-thresholds.ts
          pure functions — no inline threshold decisions here. */}
      <section aria-labelledby="composition-row-heading" className="space-y-4">
        <h2 id="composition-row-heading" className="sr-only">
          Status summary
        </h2>
        <div className="grid gap-4 xl:gap-6 grid-cols-1 md:grid-cols-2 xl:grid-cols-3">
          <StorageStatusCard
            isLoading={storageLoading}
            isError={storageIsError}
            error={storageError}
            data={storageData}
            onRetry={() => void storageRefetch()}
          />
          <RecentFailuresCard
            isLoading={isLoading}
            isError={dashIsError}
            error={dashError}
            count={failuresData.count}
            latest={failuresData.latest}
            isSuperAdmin={isSuperAdmin}
            onRetry={() => void dashRefetch()}
          />
          <ScanFindingsTrendCard
            isLoading={isLoading}
            isError={dashIsError}
            error={dashError}
            criticalCount={criticalCount}
            highCount={findings?.high ?? 0}
            neverScanned={neverScanned}
            onRetry={() => void dashRefetch()}
          />
          {isSuperAdmin && (
            <>
              <BackgroundJobsCard
                isLoading={jobsQ.isLoading}
                isError={jobsQ.isError}
                error={jobsQ.error}
                data={jobsQ.data}
                onRetry={() => void jobsQ.refetch()}
              />
              <TLSCertCard
                isLoading={tlsQ.isLoading}
                isError={tlsQ.isError}
                error={tlsQ.error}
                data={tlsQ.data}
                onRetry={() => void tlsQ.refetch()}
              />
              <TrivyDBCard
                isLoading={trivyQ.isLoading}
                isError={trivyQ.isError}
                error={trivyQ.error}
                data={trivyQ.data}
                onRetry={() => void trivyQ.refetch()}
              />
            </>
          )}
        </div>
      </section>

      {/* Row 3: Full-width storage breakdown */}
      <motion.div custom={4} initial="hidden" animate="visible" variants={cardVariants}>
        {storageLoading ? (
          <SkeletonCard rows={4} />
        ) : (
          <Card>
            <CardHeader>
              <div className="flex items-center gap-2">
                <HardDrive className="size-4 text-muted-foreground" />
                <CardTitle>Storage</CardTitle>
              </div>
            </CardHeader>
            <CardContent>
              <StorageBreakdown
                totalBytes={storageData?.total_bytes ?? 0}
                usedBytes={storageData?.used_bytes ?? 0}
                repos={storageData?.repos ?? []}
              />
            </CardContent>
          </Card>
        )}
      </motion.div>

      {/* Row 4: Activity feed + high severity findings */}
      <div className="grid gap-6 lg:grid-cols-2">
        <Card>
          <CardHeader>
            <div className="flex items-center gap-2">
              <Activity className="size-4 text-muted-foreground" />
              <CardTitle>Recent Activity</CardTitle>
            </div>
          </CardHeader>
          <CardContent>
            {isLoading ? (
              <div className="space-y-3">
                {Array.from({ length: 5 }).map((_, i) => (
                  <div key={i} className="flex gap-3">
                    <Skeleton className="h-4 w-24" />
                    <Skeleton className="h-4 flex-1" />
                  </div>
                ))}
              </div>
            ) : data?.recent_activity && data.recent_activity.length > 0 ? (
              <div className="space-y-3">
                {data.recent_activity.slice(0, 20).map((event) => {
                  const href = activityTargetHref(event.action, event.target_id);
                  const content = (
                    <>
                      <span className="shrink-0 text-xs text-muted-foreground tabular-nums">
                        {formatDate(event.created_at)}
                      </span>
                      <span className="flex-1">
                        <span className="font-medium">{event.action}</span>{' '}
                        <span className="text-muted-foreground">{event.target_id}</span>
                      </span>
                    </>
                  );
                  return href ? (
                    <Link
                      key={event.id}
                      to={href}
                      className="flex items-start gap-3 text-sm rounded-md px-1 -mx-1 py-0.5 hover:bg-muted/40 transition-colors"
                    >
                      {content}
                    </Link>
                  ) : (
                    <div key={event.id} className="flex items-start gap-3 text-sm">
                      {content}
                    </div>
                  );
                })}
              </div>
            ) : (
              // Phase 7 / D-05: "No recent activity." migrated to EmptyState
              // per CONTEXT §D-05 + UI-SPEC §DashboardPage ad-hoc string
              // migrations. No CTA — activity appears implicitly from user
              // actions elsewhere.
              <EmptyState
                icon={Activity}
                title="No recent activity"
                description="Actions you and your teammates take will appear here."
              />
            )}
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <div className="flex items-center gap-2">
              <ShieldAlert className="size-4 text-destructive" />
              <CardTitle>High-Severity Findings</CardTitle>
            </div>
          </CardHeader>
          <CardContent>
            {isLoading ? (
              <div className="space-y-3">
                {Array.from({ length: 3 }).map((_, i) => (
                  <Skeleton key={i} className="h-6 w-full" />
                ))}
              </div>
            ) : data?.high_severity && data.high_severity.length > 0 ? (
              <HighSeverityList items={data.high_severity} />
            ) : (
              // Phase 7 / D-05 / E-06: the former positive zero-CVE
              // copy is a goal state (zero CVEs = win), NOT an absence-
              // of-data surface. Per UI-SPEC E-06, switch to an inline
              // StatusBadge instead of an EmptyState — the previous
              // exclamation-framed sentence is retired.
              <div className="flex items-center gap-2">
                <StatusBadge status="healthy" label="All clear" size="sm" />
                <span className="text-sm text-muted-foreground">
                  No critical or high severity findings.
                </span>
              </div>
            )}
          </CardContent>
        </Card>
      </div>
    </div>
  );
}

// =============================================================================
// Phase 7 Composition cards (D-01..D-04)
// -----------------------------------------------------------------------------
// Each card is a self-contained component so individual loading / error
// states stay local. All six follow the same pattern:
//   Card > CardHeader (CardTitle + CardAction with StatusBadge) > CardContent
// Cold load replaces the whole card with <SkeletonCard>; error replaces
// the content with <ErrorEnvelopeRenderer mode="inline">.
// =============================================================================

type CompositionCardCommon = {
  isLoading: boolean;
  isError: boolean;
  error: unknown;
  onRetry: () => void;
};

// -- C-1 Storage Status (user-visible) ---------------------------------------

function StorageStatusCard({
  isLoading,
  isError,
  error,
  data,
  onRetry,
}: CompositionCardCommon & { data: { total_bytes: number; used_bytes: number; repos: StorageRepoRow[] } | undefined }) {
  if (isLoading || !data) {
    return <SkeletonCard rows={2} />;
  }
  const used = data.used_bytes ?? 0;
  const total = data.total_bytes ?? 0;
  const variant = storageVariant(used, total);
  const topRepos = (data.repos ?? [])
    .slice(0, 3)
    .map((r) => `${r.project}/${r.name}`)
    .join(', ');

  return (
    <Card>
      <CardHeader>
        <CardTitle>Storage</CardTitle>
        <CardAction>
          <StatusBadge status={variant} label={storageStatusLabel(variant)} size="sm" />
        </CardAction>
      </CardHeader>
      <CardContent>
        {isError ? (
          <ErrorEnvelopeRenderer
            mode="inline"
            envelope={envelopeFromError(error, "We couldn't load storage.")}
            onRetry={onRetry}
          />
        ) : total <= 0 ? (
          <p className="text-sm text-muted-foreground">Disk total not reported yet.</p>
        ) : (
          <>
            <p className="text-sm tabular-nums">
              {formatBytes(used)} / {formatBytes(total)} used
            </p>
            {topRepos ? (
              <p className="mt-1 text-xs text-muted-foreground truncate">
                Top: {topRepos}
              </p>
            ) : null}
            <Button
              variant="link"
              size="sm"
              className="px-0 mt-2"
              nativeButton={false}
              render={<a href="#storage">View storage details →</a>}
            />
          </>
        )}
      </CardContent>
    </Card>
  );
}

// -- C-2 Recent Failures (user-visible) --------------------------------------

function RecentFailuresCard({
  isLoading,
  isError,
  error,
  count,
  latest,
  isSuperAdmin,
  onRetry,
}: CompositionCardCommon & {
  count: number;
  latest: DashboardActivityItem[];
  isSuperAdmin: boolean;
}) {
  if (isLoading) {
    return <SkeletonCard rows={3} />;
  }
  const variant = failuresVariant(count);

  return (
    <Card>
      <CardHeader>
        <CardTitle>Recent Failures</CardTitle>
        <CardAction>
          <StatusBadge status={variant} label={failuresStatusLabel(variant)} size="sm" />
        </CardAction>
      </CardHeader>
      <CardContent>
        {isError ? (
          <ErrorEnvelopeRenderer
            mode="inline"
            envelope={envelopeFromError(error, "We couldn't load recent activity.")}
            onRetry={onRetry}
          />
        ) : count === 0 ? (
          <p className="text-sm text-muted-foreground">
            No failures in the last 24 hours.
          </p>
        ) : (
          <>
            <p className="text-sm tabular-nums">
              {count} failed in the last 24h
            </p>
            {latest.length > 0 && (
              <ul className="mt-2 space-y-1">
                {latest.map((event) => (
                  <li
                    key={event.id}
                    className="text-xs text-muted-foreground truncate tabular-nums"
                  >
                    {formatDate(event.created_at)} · {event.action} {event.target_id}
                  </li>
                ))}
              </ul>
            )}
            {isSuperAdmin ? (
              <Button
                variant="link"
                size="sm"
                className="px-0 mt-2"
                nativeButton={false}
                render={<Link to="/admin/audit">View full audit log →</Link>}
              />
            ) : null}
          </>
        )}
      </CardContent>
    </Card>
  );
}

// -- C-3 Scan Findings Trend (user-visible) ----------------------------------

function ScanFindingsTrendCard({
  isLoading,
  isError,
  error,
  criticalCount,
  highCount,
  neverScanned,
  onRetry,
}: CompositionCardCommon & {
  criticalCount: number;
  highCount: number;
  neverScanned: boolean;
}) {
  if (isLoading) {
    return <SkeletonCard rows={3} />;
  }
  const variant = scanFindingsVariant(criticalCount, neverScanned);

  return (
    <Card>
      <CardHeader>
        <CardTitle>Scan Findings Trend</CardTitle>
        <CardAction>
          <StatusBadge
            status={variant}
            label={scanFindingsStatusLabel(variant)}
            size="sm"
          />
        </CardAction>
      </CardHeader>
      <CardContent>
        {isError ? (
          <ErrorEnvelopeRenderer
            mode="inline"
            envelope={envelopeFromError(error, "We couldn't load scan findings.")}
            onRetry={onRetry}
          />
        ) : neverScanned ? (
          <p className="text-sm text-muted-foreground">
            Trigger a scan from any repo to populate trend data.
          </p>
        ) : (
          <>
            <p className="text-sm tabular-nums">
              {criticalCount} critical, {highCount} high
            </p>
            <Button
              variant="link"
              size="sm"
              className="px-0 mt-2"
              nativeButton={false}
              render={
                <Link to="/search?kind=cve&severity=critical,high">
                  View findings →
                </Link>
              }
            />
          </>
        )}
      </CardContent>
    </Card>
  );
}

// -- C-4 Background Jobs (admin-only) ----------------------------------------

function BackgroundJobsCard({
  isLoading,
  isError,
  error,
  data,
  onRetry,
}: CompositionCardCommon & {
  data: {
    running: number;
    queued: number;
    failed_last_24h: number;
    last_completed_at: string | null;
    last_failed_at: string | null;
  } | undefined;
}) {
  if (isLoading || !data) {
    return <SkeletonCard rows={3} />;
  }
  const variant = jobsVariant(
    data.running,
    data.queued,
    data.failed_last_24h,
    data.last_completed_at,
  );
  const label = jobsStatusLabel(
    variant,
    data.running,
    data.queued,
    data.failed_last_24h,
    data.last_completed_at,
  );

  return (
    <Card>
      <CardHeader>
        <CardTitle>Background Jobs</CardTitle>
        <CardAction>
          <StatusBadge status={variant} label={label} size="sm" />
        </CardAction>
      </CardHeader>
      <CardContent>
        {isError ? (
          <ErrorEnvelopeRenderer
            mode="inline"
            envelope={envelopeFromError(error, "We couldn't load jobs status.")}
            onRetry={onRetry}
          />
        ) : (
          <>
            <p className="text-sm tabular-nums">
              {data.running} running, {data.queued} queued
            </p>
            {data.failed_last_24h > 0 && (
              <p className="mt-1 text-sm text-muted-foreground tabular-nums">
                {data.failed_last_24h} failed in last 24h
              </p>
            )}
            {data.last_completed_at && (
              <p className="mt-1 text-xs text-muted-foreground tabular-nums">
                Last completed {formatDate(data.last_completed_at)}
              </p>
            )}
            <Button
              variant="link"
              size="sm"
              className="px-0 mt-2"
              nativeButton={false}
              render={<Link to="/admin/gc">Manage garbage collection →</Link>}
            />
          </>
        )}
      </CardContent>
    </Card>
  );
}

// -- C-5 TLS Certificate (admin-only) ----------------------------------------

function TLSCertCard({
  isLoading,
  isError,
  error,
  data,
  onRetry,
}: CompositionCardCommon & {
  data: { not_after: string; source: 'self-signed' | 'uploaded'; subject: string } | undefined;
}) {
  if (isLoading) {
    return <SkeletonCard rows={2} />;
  }
  const hasUploadedCert = data?.source === 'uploaded';
  const daysRemaining = data ? daysUntil(data.not_after) : 0;
  const variant = tlsVariant(daysRemaining, hasUploadedCert);

  return (
    <Card>
      <CardHeader>
        <CardTitle>TLS Certificate</CardTitle>
        <CardAction>
          <StatusBadge status={variant} label={tlsStatusLabel(variant)} size="sm" />
        </CardAction>
      </CardHeader>
      <CardContent>
        {isError ? (
          <ErrorEnvelopeRenderer
            mode="inline"
            envelope={envelopeFromError(error, "We couldn't load TLS status.")}
            onRetry={onRetry}
          />
        ) : !hasUploadedCert ? (
          <p className="text-sm text-muted-foreground">
            Using the default self-signed certificate.
          </p>
        ) : (
          <p className="text-sm tabular-nums">
            {daysRemaining >= 0
              ? `${daysRemaining} days remaining`
              : `Expired ${Math.abs(daysRemaining)} days ago`}
          </p>
        )}
        <p className="mt-1 text-xs text-muted-foreground">
          Source: {hasUploadedCert ? 'Uploaded' : 'Self-signed'}
        </p>
        <Button
          variant="link"
          size="sm"
          className="px-0 mt-2"
          nativeButton={false}
          render={<Link to="/admin/tls">Manage certificate →</Link>}
        />
      </CardContent>
    </Card>
  );
}

// -- C-6 Trivy Database (admin-only) -----------------------------------------

function TrivyDBCard({
  isLoading,
  isError,
  error,
  data,
  onRetry,
}: CompositionCardCommon & {
  data: { version: string; source: string; age_hours: number } | undefined;
}) {
  if (isLoading) {
    return <SkeletonCard rows={2} />;
  }
  // everInitialised: a meta row exists (version non-empty and source is
  // not 'none'). The 'baked-in' source with version='unknown' still
  // counts as initialised because scans CAN run — the age is simply
  // unknown, which trivyDBVariant folds into the warning bucket when
  // ageDays is -1 (negative, below warn threshold) via its own logic.
  // To stay close to the UI-SPEC "not initialised" copy we treat the
  // source 'none' case as the only "never initialised" state.
  const everInitialised = !!data && data.source !== 'none';
  const ageDays = data && data.age_hours >= 0 ? Math.floor(data.age_hours / 24) : 0;
  const variant = trivyDBVariant(ageDays, everInitialised);

  return (
    <Card>
      <CardHeader>
        <CardTitle>Trivy Database</CardTitle>
        <CardAction>
          <StatusBadge status={variant} label={trivyDBStatusLabel(variant)} size="sm" />
        </CardAction>
      </CardHeader>
      <CardContent>
        {isError ? (
          <ErrorEnvelopeRenderer
            mode="inline"
            envelope={envelopeFromError(error, "We couldn't load Trivy status.")}
            onRetry={onRetry}
          />
        ) : !everInitialised ? (
          <p className="text-sm text-muted-foreground">
            Upload a Trivy DB tarball to enable scanning.
          </p>
        ) : data && data.age_hours < 0 ? (
          <p className="text-sm tabular-nums">Age unknown (baked-in)</p>
        ) : (
          <p className="text-sm tabular-nums">Updated {ageDays} days ago</p>
        )}
        <Button
          variant="link"
          size="sm"
          className="px-0 mt-2"
          nativeButton={false}
          render={<Link to="/admin/trivy">Manage Trivy DB →</Link>}
        />
      </CardContent>
    </Card>
  );
}

// =============================================================================
// Existing legacy components (Row 3 + Row 4 detail renderers) — unchanged.
// =============================================================================

function HighSeverityList({ items }: { items: DashboardVulnRow[] }) {
  return (
    <div className="space-y-2">
      {items.map((v, i) => (
        <Link
          key={`${v.cve_id}-${i}`}
          to={`/projects/${v.project}/${vulnRepoType(v)}/${v.repo}`}
          className="flex items-start gap-2 text-sm rounded-md px-1 -mx-1 py-0.5 hover:bg-muted/40 transition-colors"
        >
          <SeverityBadge severity={v.severity.toLowerCase()} className="mt-0.5 shrink-0" />
          <div className="min-w-0 flex-1">
            <span className="font-mono font-medium">{v.cve_id}</span>
            <span className="mx-1.5 text-muted-foreground">in</span>
            <span className="text-muted-foreground">
              {v.project}/{v.repo}
            </span>
            {v.package && (
              <span className="ml-1.5 text-xs text-muted-foreground">
                ({v.package})
              </span>
            )}
          </div>
        </Link>
      ))}
    </div>
  );
}

// vulnRepoType picks the URL protocol segment for a vulnerability row. The
// dashboard API returns the field as `repo_type`; fall back to `docker` only
// if the backend hasn't populated it yet for legacy rows.
function vulnRepoType(v: DashboardVulnRow): string {
  return v.repo_type || 'docker';
}

// activityTargetHref maps a dashboard activity row to a navigable URL using
// the action + target_id shape the backend actually emits:
//   project.*          target_id = "<name>"               → /projects/<name>
//   member.*           target_id = "<project>"            → /projects/<project>
//   repo.*             target_id = "<project>/<type>/<name>" → /projects/.../type/name
//   signing_key.*      same shape as repo.*
// Anything else (auth.*, user.*, tls.*, trivy.*) has no useful drill-through
// and stays rendered as plain text.
function activityTargetHref(action: string, targetID: string): string {
  if (!targetID) return '';
  const parts = targetID.split('/');
  if (action.startsWith('project.') || action.startsWith('member.')) {
    return parts.length >= 1 && parts[0] ? `/projects/${parts[0]}` : '';
  }
  if (action.startsWith('repo.') || action.startsWith('signing_key.')) {
    if (parts.length >= 3 && parts[0] && parts[1] && parts[2]) {
      return `/projects/${parts[0]}/${parts[1]}/${parts[2]}`;
    }
  }
  return '';
}

function StorageBreakdown({
  totalBytes,
  usedBytes,
  repos,
}: {
  totalBytes: number;
  usedBytes: number;
  repos: StorageRepoRow[];
}) {
  const percentage = totalBytes > 0 ? Math.min(Math.round((usedBytes / totalBytes) * 100), 100) : 0;
  // When total disk capacity is known, scale per-repo bars against it so
  // each bar reads as "fraction of the whole volume" — matches the top
  // gauge. Falls back to scaling against the largest repo when capacity
  // is unknown so the chart still conveys relative size.
  const maxRepoBytes = repos.length > 0 ? repos[0].size_bytes : 1;
  const scaleDenominator = totalBytes > 0 ? totalBytes : maxRepoBytes;

  return (
    <div id="storage" className="space-y-5">
      {/* Overall gauge */}
      <div className="space-y-2">
        <div className="flex items-baseline gap-2">
          <span className="text-3xl font-bold tabular-nums">{formatBytes(usedBytes)}</span>
          <span className="text-sm text-muted-foreground">
            / {totalBytes > 0 ? formatBytes(totalBytes) : 'unknown'}
          </span>
          <span className="ml-auto text-sm text-muted-foreground tabular-nums">
            {percentage}% used
          </span>
        </div>
        <Progress value={percentage}>
          <span className="sr-only">{percentage}% used</span>
        </Progress>
      </div>

      {/* Per-repo breakdown — full-width bars with label inside */}
      {repos.length === 0 ? (
        // Phase 7 / D-05: "No repositories with stored data." migrated
        // to EmptyState per CONTEXT §D-05 + UI-SPEC.
        <EmptyState
          icon={HardDrive}
          title="No stored data yet"
          description="Repositories with artifacts will appear here once data lands."
        />
      ) : (
        <div className="space-y-2">
          {repos.map((repo) => {
            // Proportional to total disk (or to the largest repo if total
            // is unknown). No minimum floor — a repo that's 0.1% of disk
            // should render as a hairline, not pretend to be 8%.
            const rawPercent =
              scaleDenominator > 0
                ? (repo.size_bytes / scaleDenominator) * 100
                : 0;
            const barPercent = Math.min(rawPercent, 100);
            const colors = repoTypeColor[repo.type] ?? defaultColor;
            return (
              <Link
                key={`${repo.project}/${repo.type}/${repo.name}`}
                to={`/projects/${repo.project}/${repo.type}/${repo.name}`}
                className="block rounded-md p-1 -m-1 hover:bg-muted/40 transition-colors"
              >
                <div className="flex items-center gap-1.5 mb-0.5">
                  <span className={`inline-block size-2 shrink-0 rounded-full ${colors.dot}`} />
                  <span className="text-xs text-muted-foreground truncate flex-1">
                    {repo.project} /{' '}
                    <span className="font-medium text-foreground">{repo.name}</span>
                    <span className="ml-1">({repo.type})</span>
                  </span>
                  <span className="text-xs font-medium tabular-nums text-muted-foreground shrink-0">
                    {formatBytes(repo.size_bytes)}
                  </span>
                </div>
                <div
                  className="relative h-2 w-full rounded bg-muted/40 overflow-hidden"
                  title={`${formatBytes(repo.size_bytes)} — ${rawPercent.toFixed(2)}% of ${totalBytes > 0 ? formatBytes(totalBytes) : 'largest repo'}`}
                >
                  <div
                    className="absolute inset-y-0 left-0 rounded"
                    style={{
                      // Bars under 0.3% collapse to sub-pixel; clamp to a
                      // 2px hairline so users still see the segment.
                      width:
                        rawPercent > 0 && rawPercent < 0.3
                          ? '2px'
                          : `${barPercent}%`,
                      backgroundColor: colors.bar,
                      opacity: 0.85,
                    }}
                  />
                </div>
              </Link>
            );
          })}
        </div>
      )}
    </div>
  );
}
