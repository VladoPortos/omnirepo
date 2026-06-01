/**
 * Admin Trash page.
 * Table with restore/purge actions, bulk operations, retention countdown.
 */

import { useState, useCallback, useMemo } from 'react';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { api } from '@/api/client';
import type { TrashEntry, TrashListResponse } from '@/api/types';
import { DataTable, type ColumnDef } from '@/components/common/DataTable';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Checkbox } from '@/components/ui/checkbox';
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
  DialogFooter,
} from '@/components/ui/dialog';
import { toast } from 'sonner';
import { formatDate } from '@/lib/format';
import { Trash2, RotateCcw, AlertTriangle } from 'lucide-react';
import { EmptyState } from '@/components/common/EmptyState';

// ---------- Drift kind helpers ----------

// DRIFT_KIND_LABEL maps the four <proto>_*_drift trash kinds emitted by
// driftpurge.Run to the short protocol token surfaced in the
// Type-column badge. Source of truth for the kind strings:
// internal/storage/trash_test.go and internal/api/admin_trash_drift.go.
const DRIFT_KIND_LABEL: Record<string, string> = {
  pypi_file_drift: 'PyPI',
  rpm_package_drift: 'RPM',
  deb_package_drift: 'APT',
  helm_chart_drift: 'Helm',
};

function driftLabel(kind: string): string | null {
  return DRIFT_KIND_LABEL[kind] ?? null;
}

// ---------- Hooks ----------

function useTrashList(cursor?: string) {
  const params: Record<string, string> = {};
  if (cursor) params.cursor = cursor;
  return useQuery({
    queryKey: ['admin', 'trash', cursor],
    queryFn: () => api.get<TrashListResponse>('/admin/trash', params),
    staleTime: 15_000,
  });
}

function useRestoreTrash() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (ids: string[]) =>
      Promise.all(ids.map((id) => api.post<void>(`/admin/trash/${id}/restore`))),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['admin', 'trash'] });
      toast.success('Item(s) restored successfully.');
    },
  });
}

function usePurgeTrash() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (ids: string[]) =>
      Promise.all(ids.map((id) => api.del<void>(`/admin/trash/${id}`))),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['admin', 'trash'] });
      toast.success('Item(s) permanently deleted.');
    },
  });
}

// ---------- Component ----------

export default function TrashPage() {
  const [cursor, setCursor] = useState<string | undefined>();
  const { data, isLoading } = useTrashList(cursor);
  const restoreMutation = useRestoreTrash();
  const purgeMutation = usePurgeTrash();

  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [purgeTarget, setPurgeTarget] = useState<string[] | null>(null);

  const items = useMemo(() => data?.items ?? [], [data?.items]);
  const allSelected = items.length > 0 && items.every((i) => selected.has(i.id));

  const toggleSelect = useCallback((id: string) => {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  }, []);

  const toggleAll = useCallback(() => {
    if (allSelected) {
      setSelected(new Set());
    } else {
      setSelected(new Set(items.map((i) => i.id)));
    }
  }, [allSelected, items]);

  const handleRestore = useCallback(
    async (ids: string[]) => {
      try {
        await restoreMutation.mutateAsync(ids);
        setSelected(new Set());
      } catch (err) {
        toast.error(err instanceof Error ? err.message : 'Failed to restore');
      }
    },
    [restoreMutation],
  );

  const handlePurge = useCallback(async () => {
    if (!purgeTarget) return;
    try {
      await purgeMutation.mutateAsync(purgeTarget);
      setSelected(new Set());
      setPurgeTarget(null);
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to purge');
    }
  }, [purgeTarget, purgeMutation]);

  const selectedIds = useMemo(() => Array.from(selected), [selected]);

  const columns: ColumnDef<TrashEntry>[] = [
    {
      id: 'checkbox',
      name: '',
      className: 'w-10',
      render: (row) => (
        <Checkbox
          checked={selected.has(row.id)}
          onCheckedChange={() => toggleSelect(row.id)}
        />
      ),
    },
    {
      id: 'name',
      name: 'Name',
      sortable: true,
      render: (row) => <span className="font-medium">{row.name}</span>,
    },
    {
      id: 'type',
      name: 'Type',
      render: (row) => {
        const drift = driftLabel(row.type);
        if (drift !== null) {
          // Drift-purge trash holders get a distinct
          // amber outline badge so operators scanning the table can spot
          // mirror-drift entries vs. user-deleted ones at a glance. The
          // outline variant + tabIndex=0 keeps the badge keyboard-
          // focusable for screen-reader users; aria-label spells out the
          // kind (the short "PyPI" label alone is too terse for AT).
          return (
            <Badge
              variant="outline"
              tabIndex={0}
              aria-label={`Drift purge: ${drift}`}
              data-testid="trash-drift-badge"
              className="border-amber-500/60 bg-amber-50 text-amber-800 dark:bg-amber-950/40 dark:text-amber-200 focus-visible:ring-amber-400/50"
            >
              Drift · {drift}
            </Badge>
          );
        }
        return (
          <Badge variant={row.type === 'repo' ? 'default' : 'secondary'}>
            {row.type}
          </Badge>
        );
      },
    },
    {
      id: 'original_location',
      name: 'Original Location',
      render: (row) => (
        <span className="text-sm text-muted-foreground">{row.original_location}</span>
      ),
    },
    {
      id: 'deleted_by',
      name: 'Deleted By',
      accessor: (row) => row.deleted_by,
    },
    {
      id: 'deleted_at',
      name: 'Deleted At',
      sortable: true,
      render: (row) => (
        <span className="text-xs text-muted-foreground">{formatDate(row.deleted_at)}</span>
      ),
    },
    {
      id: 'retention',
      name: 'Retention',
      render: (row) => (
        <span className="text-xs font-medium tabular-nums">
          {row.retention_countdown}
        </span>
      ),
    },
    {
      id: 'actions',
      name: '',
      className: 'w-28 text-right',
      render: (row) => (
        <div className="flex justify-end gap-1">
          <Button
            variant="ghost"
            size="xs"
            onClick={() => handleRestore([row.id])}
            disabled={restoreMutation.isPending}
          >
            <RotateCcw className="mr-1 size-3" data-icon="inline-start" />
            Restore
          </Button>
          <Button
            variant="ghost"
            size="xs"
            onClick={() => setPurgeTarget([row.id])}
            className="text-destructive"
            aria-label={`Purge ${row.name}`}
            title="Purge"
          >
            <Trash2 className="size-3" />
          </Button>
        </div>
      ),
    },
  ];

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-semibold">Trash</h1>
          <p className="text-sm text-muted-foreground">
            Deleted items are retained before permanent removal. Restore or purge them here.
          </p>
        </div>
      </div>

      {/* Bulk Actions Toolbar */}
      {selected.size > 0 && (
        <div className="flex items-center gap-3 rounded-lg border bg-muted/50 p-3">
          <span className="text-sm font-medium">
            {selected.size} item{selected.size !== 1 ? 's' : ''} selected
          </span>
          <Button
            variant="outline"
            size="sm"
            onClick={() => handleRestore(selectedIds)}
            disabled={restoreMutation.isPending}
          >
            <RotateCcw className="mr-1 size-3.5" data-icon="inline-start" />
            Restore Selected
          </Button>
          <Button
            variant="destructive"
            size="sm"
            onClick={() => setPurgeTarget(selectedIds)}
          >
            <Trash2 className="mr-1 size-3.5" data-icon="inline-start" />
            Purge Selected
          </Button>
          <Button
            variant="ghost"
            size="sm"
            onClick={() => setSelected(new Set())}
          >
            Clear selection
          </Button>
        </div>
      )}

      {/* Select all checkbox hint */}
      {items.length > 0 && (
        <div className="flex items-center gap-2">
          <Checkbox checked={allSelected} onCheckedChange={toggleAll} />
          <span className="text-xs text-muted-foreground">Select all</span>
        </div>
      )}

      {!isLoading && items.length === 0 ? (
        <EmptyState
          icon={Trash2}
          title="Trash is empty"
          description="Deleted items will appear here for the configured retention period."
        />
      ) : (
        <DataTable
          columns={columns}
          data={items}
          loading={isLoading}
          stickyFirstColumn
          pagination={
            data?.next_cursor
              ? {
                  cursor: data.next_cursor,
                  hasMore: !!data.next_cursor,
                  onLoadMore: () => setCursor(data.next_cursor ?? undefined),
                }
              : undefined
          }
        />
      )}

      {/* Purge Confirmation Dialog */}
      <Dialog open={!!purgeTarget} onOpenChange={(open) => { if (!open) setPurgeTarget(null); }}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle className="flex items-center gap-2">
              <AlertTriangle className="size-5 text-destructive" />
              Purge Item{purgeTarget && purgeTarget.length > 1 ? 's' : ''}
            </DialogTitle>
            <DialogDescription>
              This will permanently delete {purgeTarget?.length ?? 0} item
              {purgeTarget && purgeTarget.length > 1 ? 's' : ''}. This cannot be undone. Continue?
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setPurgeTarget(null)}>
              Cancel
            </Button>
            <Button
              variant="destructive"
              onClick={handlePurge}
              disabled={purgeMutation.isPending}
            >
              {purgeMutation.isPending ? 'Purging...' : 'Purge'}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
