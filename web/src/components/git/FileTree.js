import { jsx as _jsx, jsxs as _jsxs } from "react/jsx-runtime";
/**
 * GitHub-style file tree browser for Git repositories.
 * Shows entries with folder/file icons, size, and last commit message.
 */
import { Folder, FileText } from 'lucide-react';
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow, } from '@/components/ui/table';
import { Skeleton } from '@/components/ui/skeleton';
import { formatBytes } from '@/lib/format';
export function FileTree({ entries, loading, currentPath, onNavigate, onBack, }) {
    // Sort: folders first, then files, alphabetically within each group
    const sorted = [...entries].sort((a, b) => {
        if (a.type === 'tree' && b.type !== 'tree')
            return -1;
        if (a.type !== 'tree' && b.type === 'tree')
            return 1;
        return a.name.localeCompare(b.name);
    });
    if (loading) {
        return (_jsx("div", { className: "space-y-2", children: Array.from({ length: 6 }).map((_, i) => (_jsx(Skeleton, { className: "h-8 w-full" }, i))) }));
    }
    return (_jsxs(Table, { children: [_jsx(TableHeader, { children: _jsxs(TableRow, { children: [_jsx(TableHead, { children: "Name" }), _jsx(TableHead, { className: "w-24 text-right", children: "Size" })] }) }), _jsxs(TableBody, { children: [currentPath && onBack && (_jsx(TableRow, { children: _jsx(TableCell, { colSpan: 2, children: _jsxs("button", { className: "inline-flex items-center gap-2 text-sm text-primary hover:underline", onClick: onBack, children: [_jsx(Folder, { className: "size-4 text-muted-foreground" }), ".."] }) }) })), sorted.length === 0 && !currentPath && (_jsx(TableRow, { children: _jsx(TableCell, { colSpan: 2, className: "h-24 text-center text-muted-foreground", children: "Empty repository" }) })), sorted.map((entry) => (_jsxs(TableRow, { children: [_jsx(TableCell, { children: _jsxs("button", { className: "inline-flex items-center gap-2 text-sm text-primary hover:underline", onClick: () => onNavigate(entry), children: [entry.type === 'tree' ? (_jsx(Folder, { className: "size-4 text-blue-500" })) : (_jsx(FileText, { className: "size-4 text-muted-foreground" })), entry.name] }) }), _jsx(TableCell, { className: "text-right text-xs text-muted-foreground", children: entry.type === 'blob' ? formatBytes(entry.size) : '--' })] }, entry.path)))] })] }));
}
