/**
 * Dashboard page per D-03.
 * Row 1: Repositories, Users, Scan Findings (3 equal cards).
 * Row 2: Full-width storage breakdown with progress bar + per-repo list.
 * Row 3: Recent Activity + High-Severity Findings.
 */

import { Link } from 'react-router-dom';
import { motion } from 'framer-motion';
import {
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
import type { StorageRepoRow } from '@/api/types';

const cardVariants = {
  hidden: { opacity: 0, y: 12 },
  visible: (i: number) => ({
    opacity: 1,
    y: 0,
    transition: { delay: i * 0.05, duration: 0.2, ease: 'easeOut' as const },
  }),
};

/** Color map for repo type indicators in the storage breakdown. */
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

      {/* Row 1: Repositories, Users, Scan Findings */}
      <div className="grid gap-4 md:grid-cols-3">
        <motion.div
          custom={0}
          initial="hidden"
          animate="visible"
          variants={cardVariants}
        >
          <Card>
            <CardHeader>
              <div className="flex items-center justify-between">
                <CardTitle className="text-sm font-medium text-muted-foreground">
                  Repositories
                </CardTitle>
                <FolderGit2 className="size-4 text-muted-foreground" />
              </div>
            </CardHeader>
            <CardContent>
              {isLoading ? (
                <Skeleton className="h-8 w-16" />
              ) : (
                <p className="text-3xl font-bold tabular-nums">
                  {data?.repo_count ?? 0}
                </p>
              )}
            </CardContent>
          </Card>
        </motion.div>

        <motion.div
          custom={1}
          initial="hidden"
          animate="visible"
          variants={cardVariants}
        >
          <Card>
            <CardHeader>
              <div className="flex items-center justify-between">
                <CardTitle className="text-sm font-medium text-muted-foreground">
                  Users
                </CardTitle>
                <Users className="size-4 text-muted-foreground" />
              </div>
            </CardHeader>
            <CardContent>
              {isLoading ? (
                <Skeleton className="h-8 w-16" />
              ) : (
                <p className="text-3xl font-bold tabular-nums">
                  {data?.user_count ?? 0}
                </p>
              )}
            </CardContent>
          </Card>
        </motion.div>

        <motion.div
          custom={2}
          initial="hidden"
          animate="visible"
          variants={cardVariants}
        >
          <Card>
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
              ) : totalFindings > 0 ? (
                <div className="space-y-2">
                  <p className="text-3xl font-bold tabular-nums">{totalFindings}</p>
                  <div className="flex flex-wrap gap-2">
                    {(findings?.critical ?? 0) > 0 && (
                      <span className="flex items-center gap-1.5">
                        <SeverityBadge severity="critical" />
                        <span className="text-sm font-medium tabular-nums">
                          {findings!.critical}
                        </span>
                      </span>
                    )}
                    {(findings?.high ?? 0) > 0 && (
                      <span className="flex items-center gap-1.5">
                        <SeverityBadge severity="high" />
                        <span className="text-sm font-medium tabular-nums">
                          {findings!.high}
                        </span>
                      </span>
                    )}
                    {(findings?.medium ?? 0) > 0 && (
                      <span className="flex items-center gap-1.5">
                        <SeverityBadge severity="medium" />
                        <span className="text-sm font-medium tabular-nums">
                          {findings!.medium}
                        </span>
                      </span>
                    )}
                    {(findings?.low ?? 0) > 0 && (
                      <span className="flex items-center gap-1.5">
                        <SeverityBadge severity="low" />
                        <span className="text-sm font-medium tabular-nums">
                          {findings!.low}
                        </span>
                      </span>
                    )}
                  </div>
                </div>
              ) : (
                <p className="text-sm text-muted-foreground">No findings</p>
              )}
            </CardContent>
          </Card>
        </motion.div>
      </div>

      {/* Row 2: Full-width storage breakdown */}
      <motion.div
        custom={3}
        initial="hidden"
        animate="visible"
        variants={cardVariants}
      >
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
                <div className="space-y-2">
                  {Array.from({ length: 4 }).map((_, i) => (
                    <Skeleton key={i} className="h-6 w-full" />
                  ))}
                </div>
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

      {/* Row 3: Activity feed + high severity */}
      <div className="grid gap-6 lg:grid-cols-2">
        {/* Recent Activity */}
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
                  <div
                    key={event.id}
                    className="flex items-start gap-3 text-sm"
                  >
                    <span className="shrink-0 text-xs text-muted-foreground tabular-nums">
                      {formatDate(event.created_at)}
                    </span>
                    <span className="flex-1">
                      {event.action}{' '}
                      <span className="text-muted-foreground">
                        {event.target_id}
                      </span>
                    </span>
                  </div>
                ))}
              </div>
            ) : (
              <p className="text-sm text-muted-foreground">No recent activity.</p>
            )}
          </CardContent>
        </Card>

        {/* High-Severity Findings */}
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
            ) : findings &&
              ((findings.critical ?? 0) > 0 || (findings.high ?? 0) > 0) ? (
              <div className="space-y-2">
                {(findings.critical ?? 0) > 0 && (
                  <div className="flex items-center justify-between">
                    <SeverityBadge severity="critical" />
                    <span className="text-sm font-medium tabular-nums">
                      {findings.critical} findings
                    </span>
                  </div>
                )}
                {(findings.high ?? 0) > 0 && (
                  <div className="flex items-center justify-between">
                    <SeverityBadge severity="high" />
                    <span className="text-sm font-medium tabular-nums">
                      {findings.high} findings
                    </span>
                  </div>
                )}
                <p className="mt-2 text-xs text-muted-foreground">
                  Review affected repositories for remediation details.
                </p>
              </div>
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

/** Full-width storage breakdown with overall progress + per-repo list. */
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
    <div className="grid gap-6 lg:grid-cols-[300px_1fr]">
      {/* Left: Overall gauge */}
      <div className="space-y-3">
        <div className="flex items-baseline justify-between">
          <span className="text-3xl font-bold tabular-nums">{formatBytes(usedBytes)}</span>
          <span className="text-sm text-muted-foreground">
            / {totalBytes > 0 ? formatBytes(totalBytes) : 'unknown'}
          </span>
        </div>
        <Progress value={percentage}>
          <span className="sr-only">{percentage}% used</span>
        </Progress>
        <p className="text-xs text-muted-foreground text-right tabular-nums">
          {percentage}% used
        </p>
      </div>

      {/* Right: Per-repo breakdown */}
      <div className="space-y-2">
        {repos.length === 0 ? (
          <p className="text-sm text-muted-foreground">No repositories with stored data.</p>
        ) : (
          <div className="max-h-[300px] space-y-1.5 overflow-y-auto">
            {repos.map((repo) => {
              const repoPercent = usedBytes > 0 ? (repo.size_bytes / usedBytes) * 100 : 0;
              const dotColor = repoTypeColor[repo.type] ?? 'bg-gray-400';
              return (
                <div key={`${repo.project}/${repo.type}/${repo.name}`} className="flex items-center gap-3 text-sm">
                  <span className={`inline-block size-2.5 shrink-0 rounded-full ${dotColor}`} />
                  <span className="min-w-0 flex-1 truncate">
                    <span className="text-muted-foreground">{repo.project}</span>
                    {' / '}
                    <span className="font-medium">{repo.name}</span>
                    <span className="ml-1.5 text-xs text-muted-foreground">({repo.type})</span>
                  </span>
                  <span className="shrink-0 tabular-nums font-medium">
                    {formatBytes(repo.size_bytes)}
                  </span>
                  <div className="hidden sm:block w-24 shrink-0">
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
