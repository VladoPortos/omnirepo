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
  Upload,
  Activity,
  HardDrive,
} from 'lucide-react';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { Button } from '@/components/ui/button';
import { Skeleton } from '@/components/ui/skeleton';
import { Progress } from '@/components/ui/progress';
import { SeverityBadge } from '@/components/common/SeverityBadge';
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

const repoTypeColor: Record<string, string> = {
  docker: 'bg-blue-500',
  rpm: 'bg-red-500',
  deb: 'bg-green-500',
  pypi: 'bg-yellow-500',
  helm: 'bg-purple-500',
  git: 'bg-orange-500',
  raw: 'bg-slate-500',
  s3: 'bg-cyan-500',
};

export function DashboardPage() {
  const { data, isLoading } = useDashboard();
  const { data: storageData, isLoading: storageLoading } = useDashboardStorage();

  const findings = data?.scan_findings;
  const totalFindings =
    (findings?.critical ?? 0) +
    (findings?.high ?? 0) +
    (findings?.medium ?? 0) +
    (findings?.low ?? 0);

  return (
    <div className="space-y-6">
      {/* Header */}
      <div className="flex items-center justify-between">
        <h1 className="text-[28px] font-semibold leading-tight">Dashboard</h1>
        <div className="flex gap-2">
          <Button variant="outline" size="sm" nativeButton={false} render={<Link to="/projects" />}>
            <Plus className="mr-1.5 size-4" />
            Create Project
          </Button>
          <Button variant="outline" size="sm" nativeButton={false} render={<Link to="/projects" />}>
            <Upload className="mr-1.5 size-4" />
            Upload Artifact
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
                {isLoading ? (
                  <Skeleton className="h-8 w-16" />
                ) : (
                  <p className="text-3xl font-bold tabular-nums">{card.value}</p>
                )}
              </CardContent>
            </Card>
          </motion.div>
        ))}

        {/* Scan Findings card — same height via h-full */}
        <motion.div
          custom={3}
          initial="hidden"
          animate="visible"
          variants={cardVariants}
        >
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
              {isLoading ? (
                <Skeleton className="h-8 w-32" />
              ) : (
                <>
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
                </>
              )}
            </CardContent>
          </Card>
        </motion.div>
      </div>

      {/* Row 2: Full-width storage breakdown */}
      <motion.div custom={4} initial="hidden" animate="visible" variants={cardVariants}>
        <Card>
          <CardHeader>
            <div className="flex items-center gap-2">
              <HardDrive className="size-4 text-muted-foreground" />
              <CardTitle>Storage</CardTitle>
            </div>
          </CardHeader>
          <CardContent>
            {storageLoading ? (
              <div className="space-y-4">
                <Skeleton className="h-4 w-48" />
                <Skeleton className="h-3 w-full" />
              </div>
            ) : (
              <StorageBreakdown
                totalBytes={storageData?.total_bytes ?? 0}
                usedBytes={storageData?.used_bytes ?? 0}
                repos={storageData?.repos ?? []}
              />
            )}
          </CardContent>
        </Card>
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
              <div className="max-h-[400px] space-y-3 overflow-y-auto">
                {data.recent_activity.slice(0, 20).map((event) => (
                  <div key={event.id} className="flex items-start gap-3 text-sm">
                    <span className="shrink-0 text-xs text-muted-foreground tabular-nums">
                      {formatDate(event.created_at)}
                    </span>
                    <span className="flex-1">
                      <span className="font-medium">{event.action}</span>{' '}
                      <span className="text-muted-foreground">{event.target_id}</span>
                    </span>
                  </div>
                ))}
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
    <div className="max-h-[400px] space-y-2 overflow-y-auto">
      {items.map((v, i) => (
        <div key={`${v.cve_id}-${i}`} className="flex items-start gap-2 text-sm">
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
        </div>
      ))}
    </div>
  );
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

  return (
    <div className="grid gap-6 lg:grid-cols-[280px_1fr]">
      {/* Left: Overall gauge */}
      <div className="space-y-3">
        <div className="flex items-baseline gap-2">
          <span className="text-3xl font-bold tabular-nums">{formatBytes(usedBytes)}</span>
          <span className="text-sm text-muted-foreground">
            / {totalBytes > 0 ? formatBytes(totalBytes) : 'unknown'}
          </span>
        </div>
        <Progress value={percentage}>
          <span className="sr-only">{percentage}% used</span>
        </Progress>
        <p className="text-xs text-muted-foreground tabular-nums">{percentage}% used</p>
      </div>

      {/* Right: Per-repo breakdown — compact layout */}
      <div className="space-y-1">
        {repos.length === 0 ? (
          <p className="text-sm text-muted-foreground">No repositories with stored data.</p>
        ) : (
          <div className="max-h-[300px] space-y-1 overflow-y-auto">
            {repos.map((repo) => {
              const repoPercent = usedBytes > 0 ? (repo.size_bytes / usedBytes) * 100 : 0;
              const dotColor = repoTypeColor[repo.type] ?? 'bg-gray-400';
              return (
                <div
                  key={`${repo.project}/${repo.type}/${repo.name}`}
                  className="flex items-center gap-2 text-sm py-0.5"
                >
                  <span className={`inline-block size-2 shrink-0 rounded-full ${dotColor}`} />
                  <span className="min-w-0 truncate text-muted-foreground">
                    {repo.project} /{' '}
                    <span className="font-medium text-foreground">{repo.name}</span>
                    <span className="ml-1 text-xs">({repo.type})</span>
                  </span>
                  <span className="ml-auto shrink-0 tabular-nums font-medium text-xs">
                    {formatBytes(repo.size_bytes)}
                  </span>
                  <div className="w-16 shrink-0">
                    <div className="h-1.5 w-full rounded-full bg-muted overflow-hidden">
                      <div
                        className={`h-full rounded-full ${dotColor}`}
                        style={{ width: `${Math.max(repoPercent, 1)}%` }}
                      />
                    </div>
                  </div>
                </div>
              );
            })}
          </div>
        )}
      </div>
    </div>
  );
}
