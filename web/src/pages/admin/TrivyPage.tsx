/**
 * Admin Trivy Database page (D-20).
 * Status card, upload dropzone, pull from internet, history table.
 */

import { useState, useCallback, useEffect, useRef } from 'react';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { api, ApiError } from '@/api/client';
import type { TrivyDBStatus, TrivyDBPullStatus } from '@/api/types';
import { DataTable, type ColumnDef } from '@/components/common/DataTable';
import { Dropzone } from '@/components/common/Dropzone';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { Progress } from '@/components/ui/progress';
import { toast } from 'sonner';
import { formatDate } from '@/lib/format';
import {
  Database,
  Upload,
  Globe,
  AlertTriangle,
  Clock,
  Loader2,
  HardDrive,
  FolderOpen,
} from 'lucide-react';

// ---------- Hooks ----------

function useTrivyDBStatus() {
  return useQuery({
    queryKey: ['admin', 'trivy', 'status'],
    queryFn: () => api.get<TrivyDBStatus>('/admin/trivy/db/status'),
    staleTime: 30_000,
  });
}

interface TrivyDBHistoryEntry {
  applied_at: string;
  source: string;
  version: string;
  size_bytes: number;
}

function useTrivyDBHistory() {
  return useQuery({
    queryKey: ['admin', 'trivy', 'history'],
    queryFn: () =>
      api.get<{ items: TrivyDBHistoryEntry[] }>('/admin/trivy/db/history'),
    staleTime: 30_000,
  });
}

function useTrivyDBPullStatus(polling: boolean) {
  return useQuery({
    queryKey: ['admin', 'trivy', 'pull-status'],
    queryFn: () => api.get<TrivyDBPullStatus>('/admin/trivy/db/pull/status'),
    refetchInterval: polling ? 500 : false,
    refetchIntervalInBackground: polling,
    staleTime: 0,
  });
}

function useTrivyDBPullStart() {
  return useMutation({
    mutationFn: () =>
      api.post<{ status: string; started_at?: string }>('/admin/trivy/db/pull'),
  });
}

// Rough estimate of a fully-downloaded Trivy DB. Used only to drive the
// progress bar's width when we have no authoritative total; clamped to
// 95 % until the job finishes so the bar never lies about completion.
// The real size depends on Trivy's DB version — historically ~100 MB,
// but recent releases pack ~1 GB. Erring high makes the bar understate
// progress rather than claim "done" too early.
const ESTIMATED_DB_BYTES = 1024 * 1024 * 1024;

// ---------- Helpers ----------

function formatAgeHours(hours: number): string {
  // The backend returns -1 when the DB directory exists but no trivy_db_meta
  // row was ever recorded (baked-in image, manual copy). Don't claim it's
  // "less than an hour" old — we simply don't know.
  if (hours < 0) return 'unknown age';
  if (hours < 1) return 'less than an hour';
  if (hours < 24) return `${Math.round(hours)} hour${Math.round(hours) !== 1 ? 's' : ''}`;
  const days = Math.floor(hours / 24);
  if (days < 7) return `${days} day${days !== 1 ? 's' : ''}`;
  const weeks = Math.floor(days / 7);
  return `${weeks} week${weeks !== 1 ? 's' : ''}`;
}

function formatFileSize(bytes: number): string {
  if (bytes === 0) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB'];
  const i = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1);
  return `${(bytes / Math.pow(1024, i)).toFixed(1)} ${units[i]}`;
}

function formatDuration(seconds: number): string {
  if (seconds < 1) return '<1s';
  if (seconds < 60) return `${Math.round(seconds)}s`;
  const m = Math.floor(seconds / 60);
  const s = Math.round(seconds % 60);
  return `${m}m ${s.toString().padStart(2, '0')}s`;
}

// ---------- Component ----------

export default function TrivyPage() {
  const { data: status, isLoading: loadingStatus } = useTrivyDBStatus();
  const { data: historyData, isLoading: loadingHistory } = useTrivyDBHistory();
  const pullStart = useTrivyDBPullStart();
  const qc = useQueryClient();

  // Poll pull-status whenever a pull is running. We start polling on click
  // and keep polling until the backend reports a terminal state; at that
  // point the final poll carries the outcome and we can stop.
  const [polling, setPolling] = useState(false);
  const { data: pullStatus } = useTrivyDBPullStatus(polling);
  const lastStateRef = useRef<TrivyDBPullStatus['state'] | undefined>(undefined);

  // Clock tick so the "Elapsed" counter updates between backend polls.
  const [, setNow] = useState(Date.now());
  useEffect(() => {
    if (!polling) return;
    const id = window.setInterval(() => setNow(Date.now()), 500);
    return () => window.clearInterval(id);
  }, [polling]);

  // If a pull is already running when the page mounts (e.g. admin navigated
  // away and back), resume polling automatically.
  useEffect(() => {
    if (pullStatus?.state === 'running' && !polling) {
      setPolling(true);
    }
  }, [pullStatus?.state, polling]);

  // React to terminal transitions: toast + invalidate cached status/history,
  // stop polling.
  useEffect(() => {
    const prev = lastStateRef.current;
    const cur = pullStatus?.state;
    if (!cur) return;
    lastStateRef.current = cur;
    if (prev === 'running' && cur === 'success') {
      toast.success('Trivy database updated from the internet.');
      qc.invalidateQueries({ queryKey: ['admin', 'trivy'] });
      setPolling(false);
    } else if (prev === 'running' && cur === 'failure') {
      toast.error(
        pullStatus?.error ??
          'Failed to pull Trivy DB. See the Trivy Database page for details.',
      );
      setPolling(false);
    }
  }, [pullStatus?.state, pullStatus?.error, qc]);

  const handlePull = useCallback(async () => {
    try {
      await pullStart.mutateAsync();
      setPolling(true);
    } catch (err) {
      if (err instanceof ApiError && err.status === 409) {
        // Another pull is already running — just start polling it.
        setPolling(true);
        return;
      }
      if (err instanceof ApiError && (err.status === 0 || err.status >= 500)) {
        toast.error(
          'Unable to start the Trivy DB pull. Check that the server is reachable.',
        );
      } else {
        toast.error(err instanceof Error ? err.message : 'Failed to start pull');
      }
    }
  }, [pullStart]);

  const handleUpload = useCallback(
    async (file: File, onProgress: (pct: number) => void) => {
      await api.upload('/admin/trivy/db', file, onProgress);
      qc.invalidateQueries({ queryKey: ['admin', 'trivy'] });
    },
    [qc],
  );

  const isStale = status?.stale ?? false;
  const ageText = status ? formatAgeHours(status.age_hours) : '';

  const historyColumns: ColumnDef<TrivyDBHistoryEntry>[] = [
    {
      id: 'applied_at',
      name: 'Date',
      sortable: true,
      render: (row) => <span className="text-sm">{formatDate(row.applied_at)}</span>,
    },
    {
      id: 'source',
      name: 'Source',
      render: (row) => (
        <Badge variant={row.source === 'online-pulled' ? 'default' : 'secondary'}>
          {row.source}
        </Badge>
      ),
    },
    {
      id: 'version',
      name: 'Version',
      accessor: (row) => row.version,
    },
    {
      id: 'size',
      name: 'Size',
      render: (row) => (
        <span className="text-sm text-muted-foreground">
          {formatFileSize(row.size_bytes)}
        </span>
      ),
    },
  ];

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-semibold">Trivy Database</h1>
        <p className="text-sm text-muted-foreground">
          Manage the vulnerability database used for artifact scanning.
        </p>
      </div>

      {/* Stale Warning — only when we can actually measure age (>= 0) and
          the backend has flagged the DB as stale. age=-1 means "no meta
          recorded" (baked-in image); in that case we show a distinct
          information banner instead of a stale warning. */}
      {isStale && status && status.age_hours >= 0 && (
        <div className="flex items-center gap-3 rounded-lg border border-amber-300 bg-amber-50 p-4 dark:border-amber-700 dark:bg-amber-950/30">
          <AlertTriangle className="size-5 shrink-0 text-amber-600 dark:text-amber-400" />
          <p className="text-sm text-amber-800 dark:text-amber-300">
            Trivy database is {ageText} old. Consider updating for the latest vulnerability data.
          </p>
        </div>
      )}
      {isStale && status && status.age_hours < 0 && (
        <div className="flex items-center gap-3 rounded-lg border border-muted bg-muted/30 p-4">
          <AlertTriangle className="size-5 shrink-0 text-muted-foreground" />
          <p className="text-sm text-muted-foreground">
            Trivy database age is unknown (no update has been recorded on this instance).
            Upload a tarball or pull from the internet to record an applied timestamp.
          </p>
        </div>
      )}

      {/* Status Card */}
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <Database className="size-5" />
            Database Status
          </CardTitle>
          <CardDescription>Current Trivy vulnerability database information</CardDescription>
        </CardHeader>
        <CardContent>
          {loadingStatus ? (
            <div className="h-20 animate-pulse rounded-md bg-muted" />
          ) : status ? (
            <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
              <div>
                <span className="text-xs text-muted-foreground">Version</span>
                <p className="font-medium font-mono">{status.version || '—'}</p>
              </div>
              <div>
                <span className="text-xs text-muted-foreground">Age</span>
                <p className="flex items-center gap-2 font-medium">
                  <Clock className="size-3.5 text-muted-foreground" />
                  {ageText}
                </p>
              </div>
              <div>
                <span className="text-xs text-muted-foreground">Source</span>
                <p>
                  <Badge
                    variant={
                      status.source === 'baked-in'
                        ? 'secondary'
                        : status.source === 'uploaded'
                          ? 'outline'
                          : status.source === 'none'
                            ? 'secondary'
                            : 'default'
                    }
                  >
                    {status.source}
                  </Badge>
                </p>
              </div>
              <div>
                <span className="text-xs text-muted-foreground">Size on disk</span>
                <p className="flex items-center gap-2 font-medium">
                  <HardDrive className="size-3.5 text-muted-foreground" />
                  {status.size_bytes !== undefined && status.size_bytes > 0
                    ? formatFileSize(status.size_bytes)
                    : '—'}
                </p>
              </div>
              <div className="sm:col-span-2">
                <span className="text-xs text-muted-foreground">Location</span>
                <p className="flex items-center gap-2 font-mono text-xs break-all">
                  <FolderOpen className="size-3.5 shrink-0 text-muted-foreground" />
                  {status.path ?? '—'}
                </p>
              </div>
            </div>
          ) : (
            <p className="text-muted-foreground">No Trivy database status available.</p>
          )}
        </CardContent>
      </Card>

      {/* Action Cards */}
      <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
        {/* Upload DB */}
        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <Upload className="size-4" />
              Upload DB
            </CardTitle>
            <CardDescription>
              Upload a Trivy database tarball for air-gapped environments.
            </CardDescription>
          </CardHeader>
          <CardContent>
            <Dropzone
              onUpload={handleUpload}
              accept=".tar.gz,.tgz,.tar"
            />
          </CardContent>
        </Card>

        {/* Pull from Internet */}
        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <Globe className="size-4" />
              Pull from Internet
            </CardTitle>
            <CardDescription>
              Download the latest Trivy database from the official server.
              Not available in air-gapped environments.
            </CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col items-stretch gap-3">
            {(() => {
              const isRunning = pullStatus?.state === 'running' || polling;
              const startedAtMs = pullStatus?.started_at
                ? new Date(pullStatus.started_at).getTime()
                : undefined;
              const elapsedSec =
                startedAtMs !== undefined
                  ? Math.max(0, (Date.now() - startedAtMs) / 1000)
                  : 0;
              const bytes = pullStatus?.bytes_downloaded ?? 0;
              const speed = elapsedSec > 0.5 ? bytes / elapsedSec : 0;
              const pctRaw = bytes / ESTIMATED_DB_BYTES;
              // Clamp to 95% while running so the bar never claims "done"
              // before the backend flips state to success.
              const pct = isRunning
                ? Math.min(95, Math.max(2, pctRaw * 100))
                : pullStatus?.state === 'success'
                  ? 100
                  : 0;
              const showFailureBox =
                pullStatus?.state === 'failure' && !!pullStatus.error;
              return (
                <>
                  <Button
                    onClick={handlePull}
                    disabled={isRunning || pullStart.isPending}
                    className="self-start"
                  >
                    {isRunning ? (
                      <>
                        <Loader2
                          className="mr-1 size-4 animate-spin"
                          data-icon="inline-start"
                        />
                        Pulling database...
                      </>
                    ) : (
                      <>
                        <Globe
                          className="mr-1 size-4"
                          data-icon="inline-start"
                        />
                        Pull Latest DB
                      </>
                    )}
                  </Button>

                  {isRunning && (
                    <div className="space-y-2 rounded-md border bg-muted/30 p-3">
                      <Progress value={pct} />
                      <div className="grid grid-cols-3 gap-2 text-xs">
                        <div>
                          <span className="text-muted-foreground">
                            Downloaded
                          </span>
                          <p className="font-medium tabular-nums">
                            {formatFileSize(bytes)}
                          </p>
                        </div>
                        <div>
                          <span className="text-muted-foreground">Elapsed</span>
                          <p className="font-medium tabular-nums">
                            {formatDuration(elapsedSec)}
                          </p>
                        </div>
                        <div>
                          <span className="text-muted-foreground">Speed</span>
                          <p className="font-medium tabular-nums">
                            {speed > 0 ? `${formatFileSize(speed)}/s` : '—'}
                          </p>
                        </div>
                      </div>
                      <p className="text-[11px] text-muted-foreground">
                        Trivy doesn't publish the total size in advance, so
                        the bar uses a ~1 GB estimate and caps at 95 % until
                        the install completes.
                      </p>
                    </div>
                  )}

                  {showFailureBox && (
                    <div className="rounded-md border border-destructive/40 bg-destructive/10 p-3 text-sm text-destructive">
                      <p className="font-medium">Last pull failed</p>
                      <p className="mt-1 break-words text-xs">
                        {pullStatus!.error}
                      </p>
                    </div>
                  )}

                  <p className="text-xs text-muted-foreground">
                    Requires outbound internet access to ghcr.io/aquasecurity.
                  </p>
                </>
              );
            })()}
          </CardContent>
        </Card>
      </div>

      {/* History */}
      <div className="space-y-3">
        <h2 className="text-lg font-semibold">Update History</h2>
        <DataTable
          columns={historyColumns}
          data={historyData?.items ?? []}
          loading={loadingHistory}
          emptyMessage="No database update history."
        />
      </div>
    </div>
  );
}
