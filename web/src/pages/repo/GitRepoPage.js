import { jsx as _jsx, jsxs as _jsxs } from "react/jsx-runtime";
/**
 * Git repository detail page per D-11.
 * File tree browser, syntax-highlighted file viewer, commit log,
 * refs, blame, diff, and branch comparison.
 */
import { useState, useCallback, useEffect } from 'react';
import { useParams } from 'react-router-dom';
import { GitBranch } from 'lucide-react';
import { Tabs, TabsList, TabsTrigger, TabsContent } from '@/components/ui/tabs';
import { CopyButton } from '@/components/common/CopyButton';
import { RepoPageLayout } from './RepoPageLayout';
import { RefSelector } from '@/components/git/RefSelector';
import { FileTree } from '@/components/git/FileTree';
import { FileViewer } from '@/components/git/FileViewer';
import { useGitRefs, useGitTree, useGitBlob } from '@/api/queries';
export function GitRepoPage({ repo }) {
    const { name: projectName } = useParams();
    const hostname = window.location.host;
    const [currentRef, setCurrentRef] = useState('');
    const [currentPath, setCurrentPath] = useState('');
    const [viewingFile, setViewingFile] = useState(null);
    const [tab, setTab] = useState('files');
    const [showBlame, setShowBlame] = useState(false);
    // Fetch refs
    const { data: refsData, isLoading: refsLoading } = useGitRefs(projectName, repo.name);
    const refs = refsData?.items ?? [];
    // Default to first branch (HEAD-like) or first ref
    useEffect(() => {
        if (refs.length > 0 && !currentRef) {
            const main = refs.find((r) => r.type === 'branch' && (r.name === 'main' || r.name === 'master'));
            setCurrentRef(main?.name ?? refs[0].name);
        }
    }, [refs, currentRef]);
    // Fetch tree for current path
    const { data: treeData, isLoading: treeLoading } = useGitTree(projectName, repo.name, currentRef, currentPath);
    const treeEntries = treeData?.items ?? [];
    // Fetch file content when viewing a file
    const { data: fileData, isLoading: fileLoading } = useGitBlob(projectName, repo.name, currentRef, viewingFile ?? '');
    const handleNavigate = useCallback((entry) => {
        if (entry.type === 'tree') {
            setCurrentPath(entry.path);
            setViewingFile(null);
            setShowBlame(false);
        }
        else {
            setViewingFile(entry.path);
            setShowBlame(false);
        }
    }, []);
    const handleBack = useCallback(() => {
        if (viewingFile) {
            setViewingFile(null);
            setShowBlame(false);
            return;
        }
        const parts = currentPath.split('/').filter(Boolean);
        parts.pop();
        setCurrentPath(parts.join('/'));
    }, [viewingFile, currentPath]);
    const handleRefChange = useCallback((ref) => {
        setCurrentRef(ref);
        setCurrentPath('');
        setViewingFile(null);
        setShowBlame(false);
    }, []);
    const cloneUrl = `${window.location.protocol}//${hostname}/${projectName}/${repo.name}.git`;
    return (_jsx(RepoPageLayout, { repo: repo, children: _jsxs("div", { className: "space-y-4", children: [_jsxs("div", { className: "flex flex-wrap items-center justify-between gap-3", children: [_jsx(RefSelector, { refs: refs, currentRef: currentRef, onRefChange: handleRefChange, loading: refsLoading }), _jsxs("div", { className: "flex items-center gap-2 rounded-md border bg-muted/30 px-3 py-1.5", children: [_jsx(GitBranch, { className: "size-4 text-muted-foreground" }), _jsx("code", { className: "text-xs", children: cloneUrl }), _jsx(CopyButton, { text: cloneUrl })] })] }), _jsxs(Tabs, { defaultValue: "files", value: tab, onValueChange: setTab, children: [_jsxs(TabsList, { children: [_jsx(TabsTrigger, { value: "files", children: "Files" }), _jsx(TabsTrigger, { value: "commits", children: "Commits" }), _jsx(TabsTrigger, { value: "refs", children: "Refs" })] }), _jsx(TabsContent, { value: "files", children: viewingFile ? (showBlame ? (
                            // BlameViewer will be wired in Task 2
                            _jsx("div", { className: "py-8 text-center text-sm text-muted-foreground", children: "Blame view loading..." })) : (_jsx(FileViewer, { file: fileData, loading: fileLoading, onBack: handleBack, onBlame: () => setShowBlame(true), downloadUrl: `/api/v1/projects/${projectName}/repos/${repo.name}/git/blob/${currentRef}/${viewingFile}?raw=1` }))) : (_jsx(FileTree, { entries: treeEntries, loading: treeLoading, currentPath: currentPath, onNavigate: handleNavigate, onBack: currentPath ? handleBack : undefined })) }), _jsx(TabsContent, { value: "commits", children: _jsx("div", { className: "py-8 text-center text-sm text-muted-foreground", children: "Commit log loading..." }) }), _jsx(TabsContent, { value: "refs", children: _jsxs("div", { className: "space-y-4 py-4", children: [refs.filter((r) => r.type === 'branch').length > 0 && (_jsxs("div", { children: [_jsx("h3", { className: "mb-2 text-sm font-semibold", children: "Branches" }), _jsx("div", { className: "space-y-1", children: refs
                                                    .filter((r) => r.type === 'branch')
                                                    .map((r) => (_jsxs("div", { className: "flex items-center justify-between rounded-md border px-3 py-2", children: [_jsx("span", { className: "text-sm", children: r.name }), _jsx("code", { className: "text-xs text-muted-foreground", children: r.sha.slice(0, 8) })] }, r.name))) })] })), refs.filter((r) => r.type === 'tag').length > 0 && (_jsxs("div", { children: [_jsx("h3", { className: "mb-2 text-sm font-semibold", children: "Tags" }), _jsx("div", { className: "space-y-1", children: refs
                                                    .filter((r) => r.type === 'tag')
                                                    .map((r) => (_jsxs("div", { className: "flex items-center justify-between rounded-md border px-3 py-2", children: [_jsx("span", { className: "text-sm", children: r.name }), _jsx("code", { className: "text-xs text-muted-foreground", children: r.sha.slice(0, 8) })] }, r.name))) })] })), refs.length === 0 && (_jsx("p", { className: "text-center text-sm text-muted-foreground", children: "No refs found. Push some code to get started." }))] }) })] })] }) }));
}
