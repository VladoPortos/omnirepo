/**
 * Admin Garbage Collection page.
 * Trigger GC button with confirmation, last run stats.
 */

import { useState, useCallback } from 'react';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { api } from '@/api/client';
import type { GCTriggerResponse, GCStatusResponse } from '@/api/types';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
  DialogFooter,
} from '@/components/ui/dialog';
import { toast } from 'sonner';
import { formatDate, formatBytes, formatDuration } from '@/lib/format';
import { Trash2, Clock, HardDrive, Loader2, FileWarning, Timer } from 'lucide-react';

// ---------- Hooks ----------

function useGCStatus() {
  return useQuery({
    queryKey: ['admin', 'gc', 'status'],
    queryFn: () => api.get<GCStatusResponse>('/admin/gc/status'),
    staleTime: 10_000,
    refetchInterval: (query) => {
      const data = query.state.data;
      // Poll every 3s while GC is running
      return data?.status === 'running' ? 3_000 : false;
    },
  });
}

function useTriggerGC() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => api.post<GCTriggerResponse>('/admin/gc'),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['admin', 'gc'] });
      toast.success('Garbage collection started.');
    },
  });
}

// ---------- Component ----------

export default function GCPage() {
  const { data: gcStatus, isLoading } = useGCStatus();
  const triggerMutation = useTriggerGC();
  const [confirmOpen, setConfirmOpen] = useState(false);

  const isRunning = gcStatus?.status === 'running';

  const handleTrigger = useCallback(async () => {
    setConfirmOpen(false);
    try {
      await triggerMutation.mutateAsync();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to trigger GC');
    }
  }, [triggerMutation]);

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-semibold">Garbage Collection</h1>
        <p className="text-sm text-muted-foreground">
          Remove orphaned blobs and expired trash entries to reclaim storage.
        </p>
      </div>

      {/* Trigger Section */}
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <Trash2 className="size-5" />
            Run Garbage Collection
          </CardTitle>
          <CardDescription>
            This will permanently delete orphan blobs and expired trash entries.
            Active uploads and recent artifacts are not affected.
          </CardDescription>
        </CardHeader>
        <CardContent>
          {isRunning ? (
            <div className="flex items-center gap-3 rounded-lg border border-blue-200 bg-blue-50 p-4 dark:border-blue-800 dark:bg-blue-950/30">
              <Loader2 className="size-5 animate-spin text-blue-600 dark:text-blue-400" />
              <span className="text-sm font-medium text-blue-800 dark:text-blue-300">
                Garbage collection in progress...
              </span>
            </div>
          ) : (
            <Button
              variant="destructive"
              onClick={() => setConfirmOpen(true)}
              disabled={triggerMutation.isPending}
            >
              <Trash2 className="mr-1 size-4" data-icon="inline-start" />
              Run Garbage Collection
            </Button>
          )}
        </CardContent>
      </Card>

      {/* Last Run Stats */}
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2 text-base">
            <Clock className="size-4" />
            Last GC Run
          </CardTitle>
        </CardHeader>
        <CardContent>
          {isLoading ? (
            <div className="h-20 animate-pulse rounded-md bg-muted" />
          ) : gcStatus && gcStatus.status !== 'idle' ? (
            <div className="grid grid-cols-2 gap-4 sm:grid-cols-4">
              <div>
                <span className="text-xs text-muted-foreground">Status</span>
                <p className="font-medium capitalize">{gcStatus.status}</p>
              </div>
              <div>
                <span className="text-xs text-muted-foreground flex items-center gap-1">
                  <HardDrive className="size-3" />
                  Bytes Freed
                </span>
                <p className="font-medium">{formatBytes(gcStatus.bytes_freed)}</p>
              </div>
              <div>
                <span className="text-xs text-muted-foreground flex items-center gap-1">
                  <Timer className="size-3" />
                  Started
                </span>
                <p className="font-medium text-sm">
                  {gcStatus.started_at ? formatDate(gcStatus.started_at) : 'N/A'}
                </p>
              </div>
              <div>
                <span className="text-xs text-muted-foreground flex items-center gap-1">
                  <FileWarning className="size-3" />
                  Finished
                </span>
                <p className="font-medium text-sm">
                  {gcStatus.finished_at ? formatDate(gcStatus.finished_at) : 'N/A'}
                </p>
              </div>
              {gcStatus.started_at && gcStatus.finished_at && (
                <div className="col-span-full">
                  <span className="text-xs text-muted-foreground">Duration</span>
                  <p className="font-medium">
                    {formatDuration(
                      new Date(gcStatus.finished_at).getTime() -
                        new Date(gcStatus.started_at).getTime(),
                    )}
                  </p>
                </div>
              )}
            </div>
          ) : (
            <p className="text-muted-foreground text-sm">
              No garbage collection has been run yet.
            </p>
          )}
        </CardContent>
      </Card>

      {/* Confirmation Dialog */}
      <Dialog open={confirmOpen} onOpenChange={setConfirmOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Run Garbage Collection</DialogTitle>
            <DialogDescription>
              This will permanently delete orphan blobs and expired trash entries. Continue?
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setConfirmOpen(false)}>
              Cancel
            </Button>
            <Button variant="destructive" onClick={handleTrigger}>
              Run GC
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
