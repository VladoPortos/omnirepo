import { jsx as _jsx, jsxs as _jsxs } from "react/jsx-runtime";
/**
 * Commit detail view showing commit info and per-file diffs.
 */
import { ArrowLeft, GitCommitHorizontal, FileDiff, Plus, Minus } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Badge } from '@/components/ui/badge';
import { Skeleton } from '@/components/ui/skeleton';
import { DiffViewer } from './DiffViewer';
import { useGitCommitDetail } from '@/api/queries';
import { formatDate } from '@/lib/format';
export function CommitDetail({ projectName, repoName, sha, onBack, }) {
    const { data, isLoading } = useGitCommitDetail(projectName, repoName, sha);
    if (isLoading) {
        return (_jsxs("div", { className: "space-y-4", children: [_jsxs(Button, { variant: "ghost", size: "sm", onClick: onBack, children: [_jsx(ArrowLeft, { className: "mr-1.5 size-4" }), "Back"] }), _jsx(Skeleton, { className: "h-20 w-full" }), _jsx(Skeleton, { className: "h-64 w-full" })] }));
    }
    if (!data) {
        return (_jsxs("div", { className: "space-y-4", children: [_jsxs(Button, { variant: "ghost", size: "sm", onClick: onBack, children: [_jsx(ArrowLeft, { className: "mr-1.5 size-4" }), "Back"] }), _jsx("p", { className: "text-center text-sm text-muted-foreground", children: "Commit not found." })] }));
    }
    return (_jsxs("div", { className: "space-y-4", children: [_jsxs(Button, { variant: "ghost", size: "sm", onClick: onBack, children: [_jsx(ArrowLeft, { className: "mr-1.5 size-4" }), "Back to commits"] }), _jsx("div", { className: "rounded-lg border p-4", children: _jsxs("div", { className: "flex items-start gap-3", children: [_jsx(GitCommitHorizontal, { className: "mt-1 size-5 text-muted-foreground" }), _jsxs("div", { className: "min-w-0 flex-1", children: [_jsx("p", { className: "text-sm font-semibold whitespace-pre-wrap", children: data.message }), _jsxs("p", { className: "mt-1 text-xs text-muted-foreground", children: ["Committed ", formatDate(data.stats ? '' : '')] }), _jsxs("div", { className: "mt-2 flex flex-wrap items-center gap-2", children: [_jsx("code", { className: "rounded bg-muted px-2 py-0.5 font-mono text-xs", children: data.sha.slice(0, 10) }), _jsxs(Badge, { variant: "outline", className: "text-xs", children: [_jsx(FileDiff, { className: "mr-1 size-3" }), data.stats.files_changed, " files"] }), _jsxs(Badge, { variant: "outline", className: "text-xs text-green-600", children: [_jsx(Plus, { className: "mr-0.5 size-3" }), data.stats.additions] }), _jsxs(Badge, { variant: "outline", className: "text-xs text-red-600", children: [_jsx(Minus, { className: "mr-0.5 size-3" }), data.stats.deletions] })] })] })] }) }), _jsx("div", { className: "space-y-4", children: data.files.map((file) => {
                    // Parse unified diff patch into old/new content
                    const { oldContent, newContent } = parsePatch(file.patch);
                    return (_jsxs("div", { className: "rounded-lg border", children: [_jsxs("div", { className: "flex items-center gap-2 border-b bg-muted/30 px-3 py-2", children: [_jsx(FileDiff, { className: "size-4 text-muted-foreground" }), _jsx("span", { className: "text-sm font-medium", children: file.path }), _jsx(Badge, { variant: "outline", className: "ml-auto text-xs", children: file.status })] }), _jsx("div", { className: "overflow-x-auto", children: _jsx(DiffViewer, { oldValue: oldContent, newValue: newContent, oldTitle: `a/${file.path}`, newTitle: `b/${file.path}` }) })] }, file.path));
                }) })] }));
}
/**
 * Minimal unified diff parser: extracts old and new file content from a patch string.
 */
function parsePatch(patch) {
    if (!patch)
        return { oldContent: '', newContent: '' };
    const lines = patch.split('\n');
    const oldLines = [];
    const newLines = [];
    for (const line of lines) {
        if (line.startsWith('@@'))
            continue;
        if (line.startsWith('---'))
            continue;
        if (line.startsWith('+++'))
            continue;
        if (line.startsWith('-')) {
            oldLines.push(line.slice(1));
        }
        else if (line.startsWith('+')) {
            newLines.push(line.slice(1));
        }
        else if (line.startsWith(' ')) {
            oldLines.push(line.slice(1));
            newLines.push(line.slice(1));
        }
        else {
            // Context line without prefix
            oldLines.push(line);
            newLines.push(line);
        }
    }
    return {
        oldContent: oldLines.join('\n'),
        newContent: newLines.join('\n'),
    };
}
