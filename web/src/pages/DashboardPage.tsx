/**
 * Dashboard page per D-03.
 * Row 1: Projects, Repositories, Users, Scan Findings (4 equal-height cards).
 * Row 2: Full-width storage breakdown with progress bar + per-repo list.
 * Row 3: Recent Activity + High-Severity Findings (with CVE details).
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
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { Button } from '@/components/ui/button';
import { Skeleton } from '@/components/ui/skeleton';
import { Progress } from '@/components/ui/progress';
import { SeverityBadge } from '@/components/common/SeverityBadge';
import { SkeletonCard } from '@/components/common/SkeletonCard';
import { SkeletonMetric } from '@/components/common/SkeletonMetric';
import { useDashboard, useDashboardStorage } from '@/api/queries';
import { formatBytes, formatDate } from '@/lib/format';
import type { StorageRepoRow, DashboardVulnRow } from '@/api/types';

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

export function DashboardPage() {
  const { data, isLoading } = useDashboard();
  const { data: storageData, isLoading: storageLoading } = useDashboardStorage();

  const findings = data?.scan_findings;
  const totalFindings =
    (findings?.critical ?? 0) +
    (findings?.high ?? 0) +
    (findings?.medium ?? 0) +
    (findings?.low ?? 0);

  // Full-page loading state uses the Phase 6 canonical Skeleton* primitives
  // (VISUAL-03). Once any dashboard slice resolves, we fall through to the
  // real layout below where per-slice micro-skeletons handle independently
  // loading tiles (storage, activity, severity lists).
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

        {/* Row 2: Storage breakdown */}
        <SkeletonCard rows={4} />

        {/* Row 3: Activity + severity */}
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

      {/* Row 2: Full-width storage breakdown */}
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

      {/* Row 3: Activity feed + high severity findings */}
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
              <p className="text-sm text-muted-foreground">No recent activity.</p>
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
              <p className="text-sm text-muted-foreground">
                No critical or high severity findings. Looking good!
              </p>
            )}
          </CardContent>
        </Card>
      </div>
    </div>
  );
}

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
  const maxRepoBytes = repos.length > 0 ? repos[0].size_bytes : 1;

  return (
    <div className="space-y-5">
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
        <p className="text-sm text-muted-foreground">No repositories with stored data.</p>
      ) : (
        <div className="space-y-2">
          {repos.map((repo) => {
            const barPercent = maxRepoBytes > 0
              ? Math.max((repo.size_bytes / maxRepoBytes) * 100, 8)
              : 8;
            const colors = repoTypeColor[repo.type] ?? defaultColor;
            return (
              <Link
                key={`${repo.project}/${repo.type}/${repo.name}`}
                to={`/projects/${repo.project}/${repo.type}/${repo.name}`}
                className="block rounded-md p-1 -m-1 hover:bg-muted/40 transition-colors"
              >
                <div className="flex items-center gap-1.5 mb-0.5">
                  <span className={`inline-block size-2 shrink-0 rounded-full ${colors.dot}`} />
                  <span className="text-xs text-muted-foreground truncate">
                    {repo.project} /{' '}
                    <span className="font-medium text-foreground">{repo.name}</span>
                    <span className="ml-1">({repo.type})</span>
                  </span>
                </div>
                <div className="relative h-6 w-full rounded bg-muted/40 overflow-hidden">
                  <div
                    className="absolute inset-y-0 left-0 rounded"
                    style={{ width: `${barPercent}%`, backgroundColor: colors.bar, opacity: 0.8 }}
                  />
                  <span className="absolute inset-y-0 left-2 flex items-center text-xs font-medium tabular-nums text-foreground drop-shadow-sm">
                    {formatBytes(repo.size_bytes)}
                  </span>
                </div>
              </Link>
            );
          })}
        </div>
      )}
    </div>
  );
}
