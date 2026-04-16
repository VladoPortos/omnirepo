import { jsx as _jsx, jsxs as _jsxs } from "react/jsx-runtime";
/**
 * Scrollable commit history for Git repositories.
 * Shows SHA, author, message, and relative date per commit.
 */
import { useState } from 'react';
import { GitCommitHorizontal, ChevronRight } from 'lucide-react';
import { Avatar, AvatarFallback } from '@/components/ui/avatar';
import { Button } from '@/components/ui/button';
import { Skeleton } from '@/components/ui/skeleton';
import { formatDate } from '@/lib/format';
import { useGitCommits } from '@/api/queries';
export function CommitLog({ projectName, repoName, currentRef, onCommitClick, }) {
    const [cursor, setCursor] = useState(undefined);
    const { data, isLoading } = useGitCommits(projectName, repoName, currentRef, cursor);
    const commits = data?.items ?? [];
    const hasMore = !!data?.next_cursor;
    if (isLoading && commits.length === 0) {
        return (_jsx("div", { className: "space-y-3 py-4", children: Array.from({ length: 5 }).map((_, i) => (_jsxs("div", { className: "flex items-center gap-3", children: [_jsx(Skeleton, { className: "size-8 rounded-full" }), _jsxs("div", { className: "flex-1 space-y-1", children: [_jsx(Skeleton, { className: "h-4 w-3/4" }), _jsx(Skeleton, { className: "h-3 w-1/2" })] })] }, i))) }));
    }
    if (commits.length === 0) {
        return (_jsx("div", { className: "py-8 text-center text-sm text-muted-foreground", children: "No commits found on this ref." }));
    }
    return (_jsxs("div", { className: "space-y-1 py-2", children: [commits.map((commit) => (_jsxs("button", { className: "flex w-full items-start gap-3 rounded-md px-3 py-2.5 text-left transition-colors hover:bg-muted/50", onClick: () => onCommitClick(commit.sha), children: [_jsx(Avatar, { className: "mt-0.5 size-7", children: _jsx(AvatarFallback, { className: "text-xs", children: commit.author_name
                                .split(' ')
                                .map((n) => n[0])
                                .join('')
                                .slice(0, 2)
                                .toUpperCase() }) }), _jsxs("div", { className: "min-w-0 flex-1", children: [_jsx("p", { className: "truncate text-sm font-medium", children: commit.message.split('\n')[0] }), _jsxs("p", { className: "text-xs text-muted-foreground", children: [commit.author_name, " committed ", formatDate(commit.author_date)] })] }), _jsxs("div", { className: "flex shrink-0 items-center gap-2", children: [_jsxs("code", { className: "rounded bg-muted px-1.5 py-0.5 font-mono text-xs text-muted-foreground", children: [_jsx(GitCommitHorizontal, { className: "mr-1 inline size-3" }), commit.sha.slice(0, 7)] }), _jsx(ChevronRight, { className: "size-4 text-muted-foreground" })] })] }, commit.sha))), hasMore && (_jsx("div", { className: "flex justify-center pt-2", children: _jsx(Button, { variant: "outline", size: "sm", onClick: () => setCursor(data?.next_cursor ?? undefined), disabled: isLoading, children: "Load more commits" }) }))] }));
}
