import { jsx as _jsx, jsxs as _jsxs } from "react/jsx-runtime";
/**
 * Branch comparison view: select two refs and show commits + file diffs.
 */
import { useState } from 'react';
import { GitCompareArrows, FileDiff } from 'lucide-react';
import { Badge } from '@/components/ui/badge';
import { Skeleton } from '@/components/ui/skeleton';
import { RefSelector } from './RefSelector';
import { DiffViewer } from './DiffViewer';
import { useGitCompare } from '@/api/queries';
import { formatDate } from '@/lib/format';
export function BranchCompare({ projectName, repoName, refs, defaultBase = '', defaultHead = '', }) {
    const [baseRef, setBaseRef] = useState(defaultBase);
    const [headRef, setHeadRef] = useState(defaultHead);
    const canCompare = baseRef && headRef && baseRef !== headRef;
    const { data, isLoading } = useGitCompare(projectName, repoName, canCompare ? baseRef : '', canCompare ? headRef : '');
    return (_jsxs("div", { className: "space-y-4 py-4", children: [_jsxs("div", { className: "flex flex-wrap items-center gap-3", children: [_jsxs("div", { className: "space-y-1", children: [_jsx("label", { className: "text-xs font-medium text-muted-foreground", children: "Base" }), _jsx(RefSelector, { refs: refs, currentRef: baseRef, onRefChange: setBaseRef })] }), _jsx(GitCompareArrows, { className: "mt-4 size-5 text-muted-foreground" }), _jsxs("div", { className: "space-y-1", children: [_jsx("label", { className: "text-xs font-medium text-muted-foreground", children: "Compare" }), _jsx(RefSelector, { refs: refs, currentRef: headRef, onRefChange: setHeadRef })] })] }), !canCompare && (_jsx("p", { className: "text-sm text-muted-foreground", children: "Select two different refs to compare." })), isLoading && canCompare && (_jsxs("div", { className: "space-y-3", children: [_jsx(Skeleton, { className: "h-16 w-full" }), _jsx(Skeleton, { className: "h-64 w-full" })] })), data && (_jsxs("div", { className: "space-y-4", children: [_jsxs("div", { className: "flex flex-wrap items-center gap-3 rounded-lg border p-3", children: [_jsxs(Badge, { variant: "outline", children: [data.ahead_by, " ahead"] }), _jsxs(Badge, { variant: "outline", children: [data.behind_by, " behind"] }), _jsxs("span", { className: "text-sm text-muted-foreground", children: [data.commits.length, " commits, ", data.files.length, " files changed"] })] }), data.commits.length > 0 && (_jsxs("div", { className: "space-y-1 rounded-lg border p-3", children: [_jsx("h3", { className: "mb-2 text-sm font-semibold", children: "Commits" }), data.commits.map((commit) => (_jsxs("div", { className: "flex items-center gap-3 py-1.5 text-sm", children: [_jsx("code", { className: "shrink-0 font-mono text-xs text-muted-foreground", children: commit.sha.slice(0, 7) }), _jsx("span", { className: "min-w-0 flex-1 truncate", children: commit.message.split('\n')[0] }), _jsx("span", { className: "shrink-0 text-xs text-muted-foreground", children: formatDate(commit.author_date) })] }, commit.sha)))] })), _jsx("div", { className: "space-y-4", children: data.files.map((file) => {
                            const { oldContent, newContent } = parsePatch(file.patch);
                            return (_jsxs("div", { className: "rounded-lg border", children: [_jsxs("div", { className: "flex items-center gap-2 border-b bg-muted/30 px-3 py-2", children: [_jsx(FileDiff, { className: "size-4 text-muted-foreground" }), _jsx("span", { className: "text-sm font-medium", children: file.path }), _jsx(Badge, { variant: "outline", className: "ml-auto text-xs", children: file.status })] }), _jsx("div", { className: "overflow-x-auto", children: _jsx(DiffViewer, { oldValue: oldContent, newValue: newContent, oldTitle: `${baseRef}:${file.path}`, newTitle: `${headRef}:${file.path}` }) })] }, file.path));
                        }) })] }))] }));
}
function parsePatch(patch) {
    if (!patch)
        return { oldContent: '', newContent: '' };
    const lines = patch.split('\n');
    const oldLines = [];
    const newLines = [];
    for (const line of lines) {
        if (line.startsWith('@@') || line.startsWith('---') || line.startsWith('+++'))
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
            oldLines.push(line);
            newLines.push(line);
        }
    }
    return { oldContent: oldLines.join('\n'), newContent: newLines.join('\n') };
}
