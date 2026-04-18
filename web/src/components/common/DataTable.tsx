/**
 * Reusable sortable table component wrapping shadcn Table per D-02.
 * Supports sorting, loading skeletons, and cursor-based pagination.
 */

import { type ReactNode } from 'react';
import { ArrowUpDown, ArrowUp, ArrowDown } from 'lucide-react';
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table';
import { Skeleton } from '@/components/ui/skeleton';
import { Button } from '@/components/ui/button';

export interface ColumnDef<T> {
  id: string;
  name: string;
  accessor?: (row: T) => unknown;
  sortable?: boolean;
  render?: (row: T) => ReactNode;
  className?: string;
}

export type SortDirection = 'asc' | 'desc' | null;

export interface SortState {
  column: string;
  direction: SortDirection;
}

interface PaginationProps {
  cursor: string | null;
  hasMore: boolean;
  onLoadMore: () => void;
}

interface DataTableProps<T> {
  columns: ColumnDef<T>[];
  data: T[];
  loading?: boolean;
  pagination?: PaginationProps;
  sort?: SortState;
  onSort?: (column: string, direction: SortDirection) => void;
  skeletonRows?: number;
  emptyMessage?: string;
  /**
   * When true, wrap the table in `<div class="overflow-x-auto
   * rounded-lg border">` and pin the first column to the left via
   * `sticky left-0 z-10 bg-card` so horizontal scroll at 1366×768 stays
   * inside the container instead of pushing the whole page (VISUAL-06 /
   * Phase 6 plan 07 admin-table pattern). Consumers with 6+ columns opt
   * in; narrow tables (<6 cols) leave it off to avoid unnecessary
   * chrome.
   */
  stickyFirstColumn?: boolean;
  onRowClick?: (row: T) => void;
  /**
   * F-T17 accordion detail row. When both isRowExpanded(row) returns true
   * AND renderExpanded is provided, the table inserts a full-width <tr>
   * immediately after the expanded row containing renderExpanded(row).
   * Scroll-margin-top on the expanded row keeps it visible when the
   * content pushes its container downward.
   */
  isRowExpanded?: (row: T) => boolean;
  renderExpanded?: (row: T) => ReactNode;
}

export function DataTable<T>({
  columns,
  data,
  loading = false,
  pagination,
  sort,
  onSort,
  skeletonRows = 5,
  emptyMessage = 'No data found.',
  stickyFirstColumn = false,
  onRowClick,
  isRowExpanded,
  renderExpanded,
}: DataTableProps<T>) {
  const handleSort = (columnId: string) => {
    if (!onSort) return;
    if (sort?.column === columnId) {
      const next: SortDirection =
        sort.direction === 'asc' ? 'desc' : sort.direction === 'desc' ? null : 'asc';
      onSort(columnId, next);
    } else {
      onSort(columnId, 'asc');
    }
  };

  const getSortIcon = (columnId: string) => {
    if (sort?.column !== columnId || !sort.direction) {
      return <ArrowUpDown className="size-3.5 opacity-50" />;
    }
    return sort.direction === 'asc' ? (
      <ArrowUp className="size-3.5" />
    ) : (
      <ArrowDown className="size-3.5" />
    );
  };

  // When stickyFirstColumn is active, the first column's header and
  // body cells carry `sticky left-0 z-10 bg-card` so they stay fixed
  // while the remaining columns scroll horizontally inside the
  // overflow-x-auto wrapper. The first-column sticky class is merged
  // (not replaced) so column-specific className props still apply.
  const stickyCellClass = 'sticky left-0 z-10 bg-card';
  const firstColClassName = (colClass?: string) =>
    stickyFirstColumn
      ? colClass
        ? `${stickyCellClass} ${colClass}`
        : stickyCellClass
      : colClass;

  const tableContent = (
    <Table className={stickyFirstColumn ? 'min-w-full' : undefined}>
      <TableHeader>
        <TableRow>
          {columns.map((col, ci) => (
            <TableHead
              key={col.id}
              className={ci === 0 ? firstColClassName(col.className) : col.className}
            >
              {col.sortable && onSort ? (
                <button
                  className="inline-flex items-center gap-1 hover:text-foreground"
                  onClick={() => handleSort(col.id)}
                >
                  {col.name}
                  {getSortIcon(col.id)}
                </button>
              ) : (
                col.name
              )}
            </TableHead>
          ))}
        </TableRow>
      </TableHeader>
      <TableBody>
        {loading
          ? Array.from({ length: skeletonRows }).map((_, i) => (
              <TableRow key={`skeleton-${i}`}>
                {columns.map((col, ci) => (
                  <TableCell
                    key={col.id}
                    className={ci === 0 ? firstColClassName() : undefined}
                  >
                    <Skeleton className="h-4 w-full max-w-[120px]" />
                  </TableCell>
                ))}
              </TableRow>
            ))
          : data.length === 0
            ? (
                <TableRow>
                  <TableCell
                    colSpan={columns.length}
                    className="h-24 text-center text-muted-foreground"
                  >
                    {emptyMessage}
                  </TableCell>
                </TableRow>
              )
            : data.flatMap((row, i) => {
                const expanded =
                  !!(renderExpanded && isRowExpanded?.(row));
                const rows = [
                  <TableRow
                    key={`r-${i}`}
                    onClick={onRowClick ? () => onRowClick(row) : undefined}
                    className={[
                      onRowClick ? 'cursor-pointer' : '',
                      // scroll-margin keeps the clicked row below the app
                      // bar when the expanded panel pushes the viewport.
                      expanded ? 'scroll-mt-16' : '',
                    ]
                      .filter(Boolean)
                      .join(' ')}
                  >
                    {columns.map((col, ci) => (
                      <TableCell
                        key={col.id}
                        className={
                          ci === 0
                            ? firstColClassName(col.className)
                            : col.className
                        }
                      >
                        {col.render
                          ? col.render(row)
                          : col.accessor
                            ? String(col.accessor(row) ?? '')
                            : null}
                      </TableCell>
                    ))}
                  </TableRow>,
                ];
                if (expanded && renderExpanded) {
                  rows.push(
                    <TableRow
                      key={`r-${i}-exp`}
                      className="bg-muted/30 hover:bg-muted/30"
                    >
                      <TableCell colSpan={columns.length} className="p-4">
                        {renderExpanded(row)}
                      </TableCell>
                    </TableRow>,
                  );
                }
                return rows;
              })}
      </TableBody>
    </Table>
  );

  return (
    <div className="space-y-2">
      {stickyFirstColumn ? (
        <div className="overflow-x-auto rounded-lg border">{tableContent}</div>
      ) : (
        tableContent
      )}

      {pagination && pagination.hasMore && !loading && (
        <div className="flex justify-center pt-2">
          <Button variant="outline" size="sm" onClick={pagination.onLoadMore}>
            Load more
          </Button>
        </div>
      )}
    </div>
  );
}
