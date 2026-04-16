/**
 * Admin Audit Log page (D-19).
 * Filterable table with detail drawer showing full JSON.
 */

import { useState, useCallback, useMemo } from 'react';
import { useQuery } from '@tanstack/react-query';
import { api } from '@/api/client';
import type { AuditEvent, AuditListResponse } from '@/api/types';
import { DataTable, type ColumnDef } from '@/components/common/DataTable';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select';
import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
  SheetDescription,
} from '@/components/ui/sheet';
import { ScrollArea } from '@/components/ui/scroll-area';
import { formatDate } from '@/lib/format';
import { Download, Filter, X } from 'lucide-react';
import { createAvatar } from '@dicebear/core';
import { initials } from '@dicebear/collection';
import { Avatar, AvatarImage, AvatarFallback } from '@/components/ui/avatar';

// ---------- Constants ----------

const ACTION_TYPES = [
  'auth.login',
  'auth.logout',
  'user.create',
  'user.update',
  'user.delete',
  'project.create',
  'project.delete',
  'repo.create',
  'repo.delete',
  'repo.wipe',
  'scan.trigger',
  'admin.gc',
  'admin.maintenance',
  'admin.tls.upload',
  'admin.trivy.upload',
  'admin.trivy.pull',
] as const;

const TARGET_KINDS = [
  'user',
  'project',
  'repo',
  'scan',
  'system',
] as const;

const OUTCOMES = ['success', 'failure'] as const;

// ---------- Hooks ----------

interface AuditFilters {
  actor?: string;
  action?: string;
  target_kind?: string;
  outcome?: string;
  from?: string;
  to?: string;
  cursor?: string;
}

function useAuditLog(filters: AuditFilters) {
  const params: Record<string, string> = {};
  if (filters.actor) params.actor = filters.actor;
  if (filters.action) params.action = filters.action;
  if (filters.target_kind) params.target_kind = filters.target_kind;
  if (filters.outcome) params.outcome = filters.outcome;
  if (filters.from) params.from = filters.from;
  if (filters.to) params.to = filters.to;
  if (filters.cursor) params.cursor = filters.cursor;

  return useQuery({
    queryKey: ['admin', 'audit', params],
    queryFn: () => api.get<AuditListResponse>('/admin/audit', params),
    staleTime: 10_000,
  });
}

// ---------- Avatar helper ----------

function ActorAvatar({ name }: { name: string }) {
  const dataUri = useMemo(() => {
    const svg = createAvatar(initials, { seed: name, size: 24 }).toString();
    return `data:image/svg+xml;utf8,${encodeURIComponent(svg)}`;
  }, [name]);

  return (
    <Avatar size="sm">
      <AvatarImage src={dataUri} alt={name} />
      <AvatarFallback>{name.charAt(0).toUpperCase()}</AvatarFallback>
    </Avatar>
  );
}

// ---------- Component ----------

export default function AuditPage() {
  const [filters, setFilters] = useState<AuditFilters>({});
  const [showFilters, setShowFilters] = useState(false);
  const { data, isLoading } = useAuditLog(filters);

  const [selectedEvent, setSelectedEvent] = useState<AuditEvent | null>(null);

  const updateFilter = useCallback(
    <K extends keyof AuditFilters>(key: K, value: AuditFilters[K]) => {
      setFilters((prev) => ({ ...prev, [key]: value, cursor: undefined }));
    },
    [],
  );

  const clearFilters = useCallback(() => {
    setFilters({});
  }, []);

  const hasActiveFilters = !!(
    filters.actor ||
    filters.action ||
    filters.target_kind ||
    filters.outcome ||
    filters.from ||
    filters.to
  );

  const handleExportJSON = useCallback(() => {
    if (!data?.items?.length) return;
    const blob = new Blob([JSON.stringify(data.items, null, 2)], {
      type: 'application/json',
    });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = `audit-log-${new Date().toISOString().slice(0, 10)}.json`;
    a.click();
    URL.revokeObjectURL(url);
  }, [data]);

  const handleExportCSV = useCallback(() => {
    if (!data?.items?.length) return;
    const headers = ['Timestamp', 'Actor', 'Action', 'Target Kind', 'Target ID', 'Outcome', 'IP'];
    const rows = data.items.map((e) => [
      e.timestamp,
      e.actor,
      e.action,
      e.target_kind,
      e.target_id,
      e.outcome,
      e.ip,
    ]);
    const csv = [headers, ...rows].map((r) => r.map((c) => `"${String(c).replace(/"/g, '""')}"`).join(',')).join('\n');
    const blob = new Blob([csv], { type: 'text/csv' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = `audit-log-${new Date().toISOString().slice(0, 10)}.csv`;
    a.click();
    URL.revokeObjectURL(url);
  }, [data]);

  const columns: ColumnDef<AuditEvent>[] = [
    {
      id: 'timestamp',
      name: 'Timestamp',
      sortable: true,
      render: (row) => (
        <span className="text-xs tabular-nums text-muted-foreground">
          {formatDate(row.timestamp)}
        </span>
      ),
    },
    {
      id: 'actor',
      name: 'Actor',
      render: (row) => (
        <div className="flex items-center gap-2">
          <ActorAvatar name={row.actor} />
          <span className="font-medium text-sm">{row.actor}</span>
        </div>
      ),
    },
    {
      id: 'action',
      name: 'Action',
      render: (row) => (
        <Badge variant="outline" className="font-mono text-xs">
          {row.action}
        </Badge>
      ),
    },
    {
      id: 'target',
      name: 'Target',
      render: (row) => (
        <span className="text-sm">
          {row.target_kind}/{row.target_id}
        </span>
      ),
    },
    {
      id: 'outcome',
      name: 'Outcome',
      render: (row) =>
        row.outcome === 'success' ? (
          <Badge variant="secondary" className="text-green-700 dark:text-green-400">
            success
          </Badge>
        ) : (
          <Badge variant="destructive">failure</Badge>
        ),
    },
    {
      id: 'ip',
      name: 'IP',
      className: 'hidden lg:table-cell',
      render: (row) => (
        <span className="text-xs text-muted-foreground font-mono">{row.ip}</span>
      ),
    },
  ];

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-semibold">Audit Log</h1>
          <p className="text-sm text-muted-foreground">
            Track all actions performed on this OmniRepo instance.
          </p>
        </div>
        <div className="flex gap-2">
          <Button
            variant="outline"
            size="sm"
            onClick={() => setShowFilters((v) => !v)}
          >
            <Filter className="mr-1 size-3.5" data-icon="inline-start" />
            Filters
            {hasActiveFilters && (
              <Badge variant="default" className="ml-1 size-4 p-0 text-[10px]">
                !
              </Badge>
            )}
          </Button>
          <Button variant="outline" size="sm" onClick={handleExportCSV}>
            <Download className="mr-1 size-3.5" data-icon="inline-start" />
            CSV
          </Button>
          <Button variant="outline" size="sm" onClick={handleExportJSON}>
            <Download className="mr-1 size-3.5" data-icon="inline-start" />
            JSON
          </Button>
        </div>
      </div>

      {/* Filter Bar */}
      {showFilters && (
        <div className="rounded-lg border bg-muted/30 p-4 space-y-3">
          <div className="flex items-center justify-between">
            <span className="text-sm font-medium">Filters</span>
            {hasActiveFilters && (
              <Button variant="ghost" size="xs" onClick={clearFilters}>
                <X className="mr-1 size-3" data-icon="inline-start" />
                Clear all
              </Button>
            )}
          </div>
          <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-6">
            <div className="space-y-1">
              <Label className="text-xs">Actor</Label>
              <Input
                placeholder="Username..."
                value={filters.actor ?? ''}
                onChange={(e) => updateFilter('actor', e.target.value || undefined)}
                className="h-8 text-sm"
              />
            </div>
            <div className="space-y-1">
              <Label className="text-xs">Action</Label>
              <Select
                value={filters.action ?? ''}
                onValueChange={(val) => updateFilter('action', val || undefined)}
              >
                <SelectTrigger className="h-8 w-full text-sm">
                  <SelectValue placeholder="All actions" />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="">All actions</SelectItem>
                  {ACTION_TYPES.map((a) => (
                    <SelectItem key={a} value={a}>
                      {a}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="space-y-1">
              <Label className="text-xs">Target Kind</Label>
              <Select
                value={filters.target_kind ?? ''}
                onValueChange={(val) => updateFilter('target_kind', val || undefined)}
              >
                <SelectTrigger className="h-8 w-full text-sm">
                  <SelectValue placeholder="All targets" />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="">All targets</SelectItem>
                  {TARGET_KINDS.map((t) => (
                    <SelectItem key={t} value={t}>
                      {t}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="space-y-1">
              <Label className="text-xs">Outcome</Label>
              <Select
                value={filters.outcome ?? ''}
                onValueChange={(val) => updateFilter('outcome', val || undefined)}
              >
                <SelectTrigger className="h-8 w-full text-sm">
                  <SelectValue placeholder="All outcomes" />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="">All outcomes</SelectItem>
                  {OUTCOMES.map((o) => (
                    <SelectItem key={o} value={o}>
                      {o}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="space-y-1">
              <Label className="text-xs">From</Label>
              <Input
                type="date"
                value={filters.from ?? ''}
                onChange={(e) => updateFilter('from', e.target.value || undefined)}
                className="h-8 text-sm"
              />
            </div>
            <div className="space-y-1">
              <Label className="text-xs">To</Label>
              <Input
                type="date"
                value={filters.to ?? ''}
                onChange={(e) => updateFilter('to', e.target.value || undefined)}
                className="h-8 text-sm"
              />
            </div>
          </div>
        </div>
      )}

      <DataTable
        columns={columns}
        data={(data?.items ?? []).map((item) => ({
          ...item,
          _onClick: () => setSelectedEvent(item),
        }))}
        loading={isLoading}
        emptyMessage="No audit events found matching your filters."
        pagination={
          data?.next_cursor
            ? {
                cursor: data.next_cursor,
                hasMore: !!data.next_cursor,
                onLoadMore: () =>
                  setFilters((prev) => ({
                    ...prev,
                    cursor: data.next_cursor ?? undefined,
                  })),
              }
            : undefined
        }
      />

      {/* Make rows clickable - wrap DataTable in a click handler */}
      {data?.items && data.items.length > 0 && (
        <div className="text-xs text-muted-foreground text-center -mt-4">
          Click a row in the table to view full details.
        </div>
      )}

      {/* Detail Sheet */}
      <Sheet open={!!selectedEvent} onOpenChange={(open) => { if (!open) setSelectedEvent(null); }}>
        <SheetContent side="right" className="sm:max-w-lg">
          <SheetHeader>
            <SheetTitle>Audit Event Details</SheetTitle>
            <SheetDescription>
              {selectedEvent?.action} by {selectedEvent?.actor} at{' '}
              {selectedEvent ? formatDate(selectedEvent.timestamp) : ''}
            </SheetDescription>
          </SheetHeader>
          {selectedEvent && (
            <ScrollArea className="flex-1 px-4 pb-4">
              <div className="space-y-4">
                <div className="grid grid-cols-2 gap-3 text-sm">
                  <div>
                    <span className="text-muted-foreground">Timestamp</span>
                    <p className="font-medium">{selectedEvent.timestamp}</p>
                  </div>
                  <div>
                    <span className="text-muted-foreground">Actor</span>
                    <p className="font-medium">{selectedEvent.actor}</p>
                  </div>
                  <div>
                    <span className="text-muted-foreground">Action</span>
                    <p className="font-medium">{selectedEvent.action}</p>
                  </div>
                  <div>
                    <span className="text-muted-foreground">Target</span>
                    <p className="font-medium">
                      {selectedEvent.target_kind}/{selectedEvent.target_id}
                    </p>
                  </div>
                  <div>
                    <span className="text-muted-foreground">Outcome</span>
                    <p className="font-medium">{selectedEvent.outcome}</p>
                  </div>
                  <div>
                    <span className="text-muted-foreground">IP Address</span>
                    <p className="font-mono text-xs">{selectedEvent.ip}</p>
                  </div>
                  <div className="col-span-2">
                    <span className="text-muted-foreground">User Agent</span>
                    <p className="font-mono text-xs break-all">
                      {selectedEvent.user_agent || 'N/A'}
                    </p>
                  </div>
                </div>

                <div>
                  <span className="text-sm text-muted-foreground">Details (JSON)</span>
                  <pre className="mt-1 overflow-x-auto rounded-md bg-muted p-3 font-mono text-xs leading-relaxed">
                    {JSON.stringify(selectedEvent.details, null, 2)}
                  </pre>
                </div>
              </div>
            </ScrollArea>
          )}
        </SheetContent>
      </Sheet>
    </div>
  );
}
