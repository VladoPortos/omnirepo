/**
 * Dashboard page per D-03.
 * Overview cards (storage, repos, users, scan findings), activity feed,
 * high-severity findings, and quick-action buttons.
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
} from 'lucide-react';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { Button } from '@/components/ui/button';
import { Skeleton } from '@/components/ui/skeleton';
import { StorageGauge } from '@/components/common/StorageGauge';
import { SeverityBadge } from '@/components/common/SeverityBadge';
import { useDashboard } from '@/api/queries';
import { formatDate } from '@/lib/format';

const cardVariants = {
  hidden: { opacity: 0, y: 12 },
  visible: (i: number) => ({
    opacity: 1,
    y: 0,
    transition: { delay: i * 0.05, duration: 0.2, ease: 'easeOut' as const },
  }),
};

export function DashboardPage() {
  const { data, isLoading } = useDashboard();

  const findings = data?.scan_findings_summary as
    | Record<string, number>
    | undefined;

  return (
    <div className="space-y-6">
      {/* Header */}
      <div className="flex items-center justify-between">
        <h1 className="text-[28px] font-semibold leading-tight">Dashboard</h1>
        <div className="flex gap-2">
          <Button variant="outline" size="sm" render={<Link to="/projects" />}>
            <Plus className="mr-1.5 size-4" />
            Create Project
          </Button>
          <Button variant="outline" size="sm" render={<Link to="/projects" />}>
            <Upload className="mr-1.5 size-4" />
            Upload Artifact
          </Button>
        </div>
      </div>

      {/* Stat cards */}
      <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-4">
        <motion.div
          custom={0}
          initial="hidden"
          animate="visible"
          variants={cardVariants}
        >
          <Card>
            <CardContent>
              {isLoading ? (
                <div className="space-y-3">
                  <Skeleton className="h-4 w-16" />
                  <Skeleton className="h-2 w-full" />
                  <Skeleton className="h-3 w-24" />
                </div>
              ) : (
                <StorageGauge
                  used={data?.storage_used ?? 0}
                  total={data?.storage_total ?? 1}
                />
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
          custom={2}
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
          custom={3}
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
              ) : findings ? (
                <div className="flex flex-wrap gap-2">
                  {Object.entries(findings).map(([sev, count]) => (
                    <span key={sev} className="flex items-center gap-1.5">
                      <SeverityBadge severity={sev} />
                      <span className="text-sm font-medium tabular-nums">
                        {count}
                      </span>
                    </span>
                  ))}
                  {Object.keys(findings).length === 0 && (
                    <p className="text-sm text-muted-foreground">No findings</p>
                  )}
                </div>
              ) : (
                <p className="text-sm text-muted-foreground">No findings</p>
              )}
            </CardContent>
          </Card>
        </motion.div>
      </div>

      {/* Activity feed + high severity */}
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
                      {formatDate(event.timestamp)}
                    </span>
                    <span className="flex-1">
                      <span className="font-medium">{event.actor}</span>{' '}
                      {event.action}{' '}
                      <span className="text-muted-foreground">
                        {event.target_kind}/{event.target_id}
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
