import { jsx as _jsx, jsxs as _jsxs } from "react/jsx-runtime";
/**
 * Syntax-highlighted file viewer for Git repositories.
 * Uses Shiki for highlighting with DOMPurify defense-in-depth (T-05-08-01).
 * Shows "File too large" for files > 1 MB (T-05-08-02).
 */
import { useState, useEffect } from 'react';
import DOMPurify from 'dompurify';
import { FileText, Download, Eye, ArrowLeft } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Skeleton } from '@/components/ui/skeleton';
import { CopyButton } from '@/components/common/CopyButton';
import { highlightCode, detectLanguage } from '@/lib/highlight';
const MAX_DISPLAY_SIZE = 1_048_576; // 1 MB
export function FileViewer({ file, loading, onBlame, onBack, downloadUrl, }) {
    const [html, setHtml] = useState('');
    const [highlighting, setHighlighting] = useState(false);
    useEffect(() => {
        if (!file?.content) {
            setHtml('');
            return;
        }
        if (file.size > MAX_DISPLAY_SIZE) {
            setHtml('');
            return;
        }
        let cancelled = false;
        setHighlighting(true);
        const content = file.encoding === 'base64'
            ? atob(file.content)
            : file.content;
        const lang = detectLanguage(file.name);
        const theme = document.documentElement.classList.contains('dark')
            ? 'github-dark'
            : 'github-light';
        highlightCode(content, lang, theme)
            .then((result) => {
            if (!cancelled) {
                // Defense-in-depth: sanitize Shiki output through DOMPurify (T-05-08-01).
                // Shiki produces only <pre>, <code>, <span> with inline styles from
                // grammar tokenization -- not user-supplied HTML. DOMPurify is an extra
                // safety layer against any future Shiki regression.
                const sanitized = DOMPurify.sanitize(result, {
                    ALLOWED_TAGS: ['pre', 'code', 'span'],
                    ALLOWED_ATTR: ['class', 'style'],
                });
                setHtml(sanitized);
            }
        })
            .catch(() => {
            if (!cancelled) {
                const escaped = content
                    .replace(/&/g, '&amp;')
                    .replace(/</g, '&lt;')
                    .replace(/>/g, '&gt;');
                setHtml(`<pre><code>${escaped}</code></pre>`);
            }
        })
            .finally(() => {
            if (!cancelled)
                setHighlighting(false);
        });
        return () => {
            cancelled = true;
        };
    }, [file]);
    if (loading) {
        return (_jsxs("div", { className: "space-y-2", children: [_jsx(Skeleton, { className: "h-6 w-48" }), _jsx(Skeleton, { className: "h-64 w-full" })] }));
    }
    if (!file) {
        return null;
    }
    // File too large guard (T-05-08-02)
    if (file.size > MAX_DISPLAY_SIZE) {
        return (_jsxs("div", { className: "space-y-4", children: [_jsx("div", { className: "flex items-center gap-2", children: _jsxs(Button, { variant: "ghost", size: "sm", onClick: onBack, children: [_jsx(ArrowLeft, { className: "mr-1.5 size-4" }), "Back"] }) }), _jsxs("div", { className: "flex flex-col items-center justify-center gap-4 rounded-lg border bg-muted/30 py-16", children: [_jsx(FileText, { className: "size-12 text-muted-foreground" }), _jsxs("p", { className: "text-sm text-muted-foreground", children: ["File too large to display (", (file.size / 1_048_576).toFixed(1), " MB)"] }), downloadUrl && (_jsxs(Button, { variant: "outline", size: "sm", render: _jsx("a", { href: downloadUrl, download: file.name }), children: [_jsx(Download, { className: "mr-1.5 size-4" }), "Download"] }))] })] }));
    }
    const rawContent = file.encoding === 'base64' ? atob(file.content) : file.content;
    const lineCount = rawContent.split('\n').length;
    return (_jsxs("div", { className: "space-y-3", children: [_jsxs("div", { className: "flex flex-wrap items-center justify-between gap-2", children: [_jsxs("div", { className: "flex items-center gap-2", children: [_jsxs(Button, { variant: "ghost", size: "sm", onClick: onBack, children: [_jsx(ArrowLeft, { className: "mr-1.5 size-4" }), "Back"] }), _jsx("span", { className: "text-sm font-medium", children: file.path }), _jsxs("span", { className: "text-xs text-muted-foreground", children: [lineCount, " lines"] })] }), _jsxs("div", { className: "flex items-center gap-1", children: [_jsx(CopyButton, { text: rawContent }), onBlame && (_jsxs(Button, { variant: "ghost", size: "sm", onClick: onBlame, children: [_jsx(Eye, { className: "mr-1.5 size-4" }), "Blame"] })), downloadUrl && (_jsxs(Button, { variant: "ghost", size: "sm", render: _jsx("a", { href: downloadUrl, download: file.name }), children: [_jsx(Download, { className: "mr-1.5 size-4" }), "Raw"] }))] })] }), _jsx("div", { className: "overflow-x-auto rounded-lg border", children: highlighting ? (_jsx("div", { className: "p-4", children: _jsx(Skeleton, { className: "h-64 w-full" }) })) : (_jsxs("div", { className: "flex", children: [_jsx("div", { className: "shrink-0 select-none border-r bg-muted/30 px-3 py-3 text-right font-mono text-xs text-muted-foreground", children: Array.from({ length: lineCount }, (_, i) => (_jsx("div", { className: "leading-5", children: i + 1 }, i + 1))) }), _jsx("div", { className: "flex-1 overflow-x-auto p-3 [&_pre]:!m-0 [&_pre]:!bg-transparent [&_pre]:!p-0 [&_code]:leading-5", dangerouslySetInnerHTML: { __html: html } })] })) })] }));
}
