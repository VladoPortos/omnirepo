import { jsx as _jsx, jsxs as _jsxs } from "react/jsx-runtime";
import { ArrowUpDown, ArrowUp, ArrowDown } from 'lucide-react';
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow, } from '@/components/ui/table';
import { Skeleton } from '@/components/ui/skeleton';
import { Button } from '@/components/ui/button';
export function DataTable({ columns, data, loading = false, pagination, sort, onSort, skeletonRows = 5, emptyMessage = 'No data found.', }) {
    const handleSort = (columnId) => {
        if (!onSort)
            return;
        if (sort?.column === columnId) {
            const next = sort.direction === 'asc' ? 'desc' : sort.direction === 'desc' ? null : 'asc';
            onSort(columnId, next);
        }
        else {
            onSort(columnId, 'asc');
        }
    };
    const getSortIcon = (columnId) => {
        if (sort?.column !== columnId || !sort.direction) {
            return _jsx(ArrowUpDown, { className: "size-3.5 opacity-50" });
        }
        return sort.direction === 'asc' ? (_jsx(ArrowUp, { className: "size-3.5" })) : (_jsx(ArrowDown, { className: "size-3.5" }));
    };
    return (_jsxs("div", { className: "space-y-2", children: [_jsxs(Table, { children: [_jsx(TableHeader, { children: _jsx(TableRow, { children: columns.map((col) => (_jsx(TableHead, { className: col.className, children: col.sortable && onSort ? (_jsxs("button", { className: "inline-flex items-center gap-1 hover:text-foreground", onClick: () => handleSort(col.id), children: [col.name, getSortIcon(col.id)] })) : (col.name) }, col.id))) }) }), _jsx(TableBody, { children: loading
                            ? Array.from({ length: skeletonRows }).map((_, i) => (_jsx(TableRow, { children: columns.map((col) => (_jsx(TableCell, { children: _jsx(Skeleton, { className: "h-4 w-full max-w-[120px]" }) }, col.id))) }, `skeleton-${i}`)))
                            : data.length === 0
                                ? (_jsx(TableRow, { children: _jsx(TableCell, { colSpan: columns.length, className: "h-24 text-center text-muted-foreground", children: emptyMessage }) }))
                                : data.map((row, i) => (_jsx(TableRow, { children: columns.map((col) => (_jsx(TableCell, { className: col.className, children: col.render
                                            ? col.render(row)
                                            : col.accessor
                                                ? String(col.accessor(row) ?? '')
                                                : null }, col.id))) }, i))) })] }), pagination && pagination.hasMore && !loading && (_jsx("div", { className: "flex justify-center pt-2", children: _jsx(Button, { variant: "outline", size: "sm", onClick: pagination.onLoadMore, children: "Load more" }) }))] }));
}
