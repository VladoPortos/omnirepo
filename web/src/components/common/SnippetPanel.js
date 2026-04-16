import { Fragment as _Fragment, jsx as _jsx, jsxs as _jsxs } from "react/jsx-runtime";
/**
 * Sheet (slide-out panel) with protocol-aware CLI commands per D-16.
 * Each line has a CopyButton.
 */
import { Sheet, SheetTrigger, SheetContent, SheetHeader, SheetTitle, SheetDescription, } from '@/components/ui/sheet';
import { Button } from '@/components/ui/button';
import { ScrollArea } from '@/components/ui/scroll-area';
import { Terminal } from 'lucide-react';
import { CopyButton } from './CopyButton';
import { getSnippets } from '@/lib/snippets';
export function SnippetPanel({ repoType, projectName, repoName, hostname, children, }) {
    const snippets = getSnippets(repoType, projectName, repoName, hostname);
    return (_jsxs(Sheet, { children: [_jsx(SheetTrigger, { render: children ? (_jsx(_Fragment, { children: children })) : (_jsxs(Button, { variant: "outline", size: "sm", children: [_jsx(Terminal, { className: "mr-1.5 size-4" }), "CLI Snippets"] })) }), _jsxs(SheetContent, { side: "right", children: [_jsxs(SheetHeader, { children: [_jsx(SheetTitle, { children: "CLI Snippets" }), _jsxs(SheetDescription, { children: ["Pre-filled commands for ", repoType, " repository", ' ', _jsxs("strong", { children: [projectName, "/", repoName] })] })] }), _jsx(ScrollArea, { className: "flex-1 px-4", children: _jsx("div", { className: "space-y-4 pb-4", children: snippets.map((snippet) => (_jsxs("div", { className: "space-y-1.5", children: [_jsx("h4", { className: "text-sm font-medium", children: snippet.label }), _jsxs("div", { className: "relative rounded-md bg-muted p-3 pr-10 font-mono text-xs", children: [_jsx("pre", { className: "overflow-x-auto whitespace-pre-wrap break-all", children: snippet.cmd }), _jsx(CopyButton, { text: snippet.cmd, className: "absolute right-1.5 top-1.5" })] })] }, snippet.label))) }) })] })] }));
}
