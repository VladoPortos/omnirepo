/**
 * Admin Trivy Database page (D-20).
 * Status card, upload dropzone, pull from internet, history table.
 */

import { useState, useCallback } from 'react';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { api, ApiError } from '@/api/client';
import type { TrivyDBStatus } from '@/api/types';
import { DataTable, type ColumnDef } from '@/components/common/DataTable';
import { Dropzone } from '@/components/common/Dropzone';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { toast } from 'sonner';
import { formatDate } from '@/lib/format';
import { Database, Upload, Globe, AlertTriangle, Clock, Loader2 } from 'lucide-react';

// ---------- Hooks ----------

function useTrivyDBStatus() {
  return useQuery({
    queryKey: ['admin', 'trivy', 'status'],
    queryFn: () => api.get<TrivyDBStatus>('/admin/trivy/db'),
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
    queryFn: () => api.get<{ items: TrivyDBHistoryEntry[] }>('/admin/trivy/db/history'),
    staleTime: 30_000,
  });
}

function useTrivyDBPull() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => api.post<void>('/admin/trivy/db/pull'),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['admin', 'trivy'] });
      toast.success('Trivy database updated from the internet.');
    },
  });
}

// ---------- Helpers ----------

function formatAgeHours(hours: number): string {
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

// ---------- Component ----------

export default function TrivyPage() {
  const { data: status, isLoading: loadingStatus } = useTrivyDBStatus();
  const { data: historyData, isLoading: loadingHistory } = useTrivyDBHistory();
  const pullMutation = useTrivyDBPull();
  const qc = useQueryClient();
  const [isPulling, setIsPulling] = useState(false);

  const handlePull = useCallback(async () => {
    setIsPulling(true);
    try {
      await pullMutation.mutateAsync();
    } catch (err) {
      if (err instanceof ApiError && (err.status === 0 || err.status >= 500)) {
        toast.error(
          'Unable to reach the Trivy database server. This is expected in air-gapped environments. Upload a DB tarball instead.',
        );
      } else {
        toast.error(err instanceof Error ? err.message : 'Failed to pull Trivy DB');
      }
    } finally {
      setIsPulling(false);
    }
  }, [pullMutation]);

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

      {/* Stale Warning */}
      {isStale && (
        <div className="flex items-center gap-3 rounded-lg border border-amber-300 bg-amber-50 p-4 dark:border-amber-700 dark:bg-amber-950/30">
          <AlertTriangle className="size-5 shrink-0 text-amber-600 dark:text-amber-400" />
          <p className="text-sm text-amber-800 dark:text-amber-300">
            Trivy database is {ageText} old. Consider updating for the latest vulnerability data.
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
            <div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
              <div>
                <span className="text-xs text-muted-foreground">Version</span>
                <p className="font-medium font-mono">{status.version}</p>
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
                          : 'default'
                    }
                  >
                    {status.source}
                  </Badge>
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
          <CardContent className="flex flex-col items-start gap-3">
            <Button
              onClick={handlePull}
              disabled={isPulling || pullMutation.isPending}
            >
              {isPulling ? (
                <>
                  <Loader2 className="mr-1 size-4 animate-spin" data-icon="inline-start" />
                  Pulling database...
                </>
              ) : (
                <>
                  <Globe className="mr-1 size-4" data-icon="inline-start" />
                  Pull Latest DB
                </>
              )}
            </Button>
            <p className="text-xs text-muted-foreground">
              Requires outbound internet access to ghcr.io/aquasecurity.
            </p>
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
