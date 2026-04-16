import { jsx as _jsx, jsxs as _jsxs } from "react/jsx-runtime";
/**
 * Per-line blame viewer for Git files.
 * Shows commit attribution alongside syntax-highlighted code.
 * Uses DOMPurify-sanitized Shiki output (same defense-in-depth as FileViewer, T-05-08-01).
 */
import { useState, useEffect } from 'react';
import DOMPurify from 'dompurify';
import { ArrowLeft } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Skeleton } from '@/components/ui/skeleton';
import { highlightCode, detectLanguage } from '@/lib/highlight';
import { formatDate } from '@/lib/format';
import { useGitBlame } from '@/api/queries';
function escapeHtml(s) {
    return s
        .replace(/&/g, '&amp;')
        .replace(/</g, '&lt;')
        .replace(/>/g, '&gt;');
}
export function BlameViewer({ projectName, repoName, currentRef, filePath, onBack, }) {
    const { data: blameData, isLoading } = useGitBlame(projectName, repoName, currentRef, filePath);
    const [highlightedLines, setHighlightedLines] = useState([]);
    const [highlighting, setHighlighting] = useState(false);
    useEffect(() => {
        if (!blameData?.lines?.length) {
            setHighlightedLines([]);
            return;
        }
        let cancelled = false;
        setHighlighting(true);
        const fullCode = blameData.lines.map((l) => l.content).join('\n');
        const lang = detectLanguage(filePath);
        const theme = document.documentElement.classList.contains('dark')
            ? 'github-dark'
            : 'github-light';
        highlightCode(fullCode, lang, theme)
            .then((html) => {
            if (cancelled)
                return;
            // Defense-in-depth: sanitize Shiki output through DOMPurify (T-05-08-01).
            // Same pattern as FileViewer -- Shiki output is grammar-tokenized, not
            // user HTML, but we sanitize as extra safety against regressions.
            const sanitized = DOMPurify.sanitize(html, {
                ALLOWED_TAGS: ['pre', 'code', 'span'],
                ALLOWED_ATTR: ['class', 'style'],
            });
            // Parse out individual lines from the highlighted HTML
            const parser = new DOMParser();
            const doc = parser.parseFromString(sanitized, 'text/html');
            const codeEl = doc.querySelector('code');
            if (codeEl) {
                const innerHtml = codeEl.innerHTML;
                const lines = innerHtml.split('\n');
                setHighlightedLines(lines);
            }
            else {
                setHighlightedLines(blameData.lines.map((l) => escapeHtml(l.content)));
            }
        })
            .catch(() => {
            if (!cancelled) {
                setHighlightedLines(blameData.lines.map((l) => escapeHtml(l.content)));
            }
        })
            .finally(() => {
            if (!cancelled)
                setHighlighting(false);
        });
        return () => {
            cancelled = true;
        };
    }, [blameData, filePath]);
    if (isLoading || highlighting) {
        return (_jsxs("div", { className: "space-y-2", children: [_jsxs("div", { className: "flex items-center gap-2", children: [_jsxs(Button, { variant: "ghost", size: "sm", onClick: onBack, children: [_jsx(ArrowLeft, { className: "mr-1.5 size-4" }), "Back"] }), _jsxs("span", { className: "text-sm font-medium", children: ["Blame: ", filePath] })] }), _jsx("div", { className: "space-y-1", children: Array.from({ length: 10 }).map((_, i) => (_jsx(Skeleton, { className: "h-5 w-full" }, i))) })] }));
    }
    if (!blameData?.lines?.length) {
        return (_jsxs("div", { className: "space-y-4", children: [_jsx("div", { className: "flex items-center gap-2", children: _jsxs(Button, { variant: "ghost", size: "sm", onClick: onBack, children: [_jsx(ArrowLeft, { className: "mr-1.5 size-4" }), "Back"] }) }), _jsx("p", { className: "text-center text-sm text-muted-foreground", children: "No blame data available for this file." })] }));
    }
    // Group consecutive lines by SHA for alternating shading
    let prevSha = '';
    let blockIndex = 0;
    return (_jsxs("div", { className: "space-y-3", children: [_jsxs("div", { className: "flex items-center gap-2", children: [_jsxs(Button, { variant: "ghost", size: "sm", onClick: onBack, children: [_jsx(ArrowLeft, { className: "mr-1.5 size-4" }), "Back"] }), _jsxs("span", { className: "text-sm font-medium", children: ["Blame: ", filePath] })] }), _jsx("div", { className: "overflow-x-auto rounded-lg border font-mono text-xs", children: _jsx("table", { className: "w-full border-collapse", children: _jsx("tbody", { children: blameData.lines.map((line, idx) => {
                            if (line.sha !== prevSha) {
                                blockIndex++;
                                prevSha = line.sha;
                            }
                            const isEvenBlock = blockIndex % 2 === 0;
                            const showBlameInfo = idx === 0 || blameData.lines[idx - 1].sha !== line.sha;
                            // Each line's highlighted HTML was already DOMPurify-sanitized above
                            const lineHtml = highlightedLines[idx] ?? escapeHtml(line.content);
                            return (_jsxs("tr", { className: isEvenBlock ? 'bg-muted/20' : '', children: [_jsx("td", { className: "w-64 shrink-0 border-r px-2 py-0 align-top", children: showBlameInfo && (_jsxs("div", { className: "flex items-center gap-2 whitespace-nowrap py-0.5", children: [_jsx("code", { className: "text-muted-foreground", children: line.sha.slice(0, 7) }), _jsx("span", { className: "max-w-24 truncate text-foreground", children: line.author }), _jsx("span", { className: "text-muted-foreground", children: formatDate(line.date) })] })) }), _jsx("td", { className: "w-10 shrink-0 select-none border-r px-2 py-0 text-right text-muted-foreground", children: line.line_number }), _jsx("td", { className: "px-2 py-0", children: _jsx("span", { className: "leading-5", 
                                            // Safe: lineHtml is DOMPurify-sanitized Shiki grammar output
                                            // or HTML-escaped plain text (escapeHtml fallback)
                                            dangerouslySetInnerHTML: { __html: lineHtml } }) })] }, idx));
                        }) }) }) })] }));
}
