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

  return (
    <div className="space-y-2">
      <Table>
        <TableHeader>
          <TableRow>
            {columns.map((col) => (
              <TableHead key={col.id} className={col.className}>
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
                  {columns.map((col) => (
                    <TableCell key={col.id}>
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
              : data.map((row, i) => (
                  <TableRow key={i}>
                    {columns.map((col) => (
                      <TableCell key={col.id} className={col.className}>
                        {col.render
                          ? col.render(row)
                          : col.accessor
                            ? String(col.accessor(row) ?? '')
                            : null}
                      </TableCell>
                    ))}
                  </TableRow>
                ))}
        </TableBody>
      </Table>

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
