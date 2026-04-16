import { jsx as _jsx, jsxs as _jsxs } from "react/jsx-runtime";
/**
 * Project detail page per D-04, D-27.
 * Breadcrumb, tabs per repo type, overview with members + activity.
 */
import { useState, useMemo } from 'react';
import { useParams, useNavigate, Link } from 'react-router-dom';
import { motion } from 'framer-motion';
import { Plus, Users, Activity, FolderGit2 } from 'lucide-react';
import { toast } from 'sonner';
import { Breadcrumb, BreadcrumbList, BreadcrumbItem, BreadcrumbLink, BreadcrumbSeparator, BreadcrumbPage, } from '@/components/ui/breadcrumb';
import { Tabs, TabsList, TabsTrigger, TabsContent } from '@/components/ui/tabs';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { Skeleton } from '@/components/ui/skeleton';
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogFooter, } from '@/components/ui/dialog';
import { Select, SelectTrigger, SelectValue, SelectContent, SelectItem, } from '@/components/ui/select';
import { Avatar, AvatarFallback } from '@/components/ui/avatar';
import { TypeBadge } from '@/components/common/TypeBadge';
import { StorageGauge } from '@/components/common/StorageGauge';
import { useProject, useProjectActivity, useCreateRepo, } from '@/api/queries';
import { formatBytes, formatDate } from '@/lib/format';
import { ApiError } from '@/api/client';
const REPO_TYPES = [
    { value: 'docker', label: 'Docker' },
    { value: 'rpm', label: 'RPM' },
    { value: 'deb', label: 'APT' },
    { value: 'pypi', label: 'PyPI' },
    { value: 'helm', label: 'Helm' },
    { value: 'git', label: 'Git' },
    { value: 'raw', label: 'RAW' },
    { value: 's3', label: 'S3' },
];
const ALL_TABS = ['overview', ...REPO_TYPES.map((t) => t.value)];
export function ProjectDetailPage() {
    const { name = '' } = useParams();
    const navigate = useNavigate();
    const { data: project, isLoading } = useProject(name);
    const { data: activityData } = useProjectActivity(name);
    const createRepo = useCreateRepo();
    const [activeTab, setActiveTab] = useState('overview');
    const [dialogOpen, setDialogOpen] = useState(false);
    const [repoName, setRepoName] = useState('');
    const [repoType, setRepoType] = useState(activeTab !== 'overview' ? activeTab : 'docker');
    const [createError, setCreateError] = useState('');
    // Group repos by type
    const reposByType = useMemo(() => {
        const map = {};
        for (const rt of REPO_TYPES) {
            map[rt.value] = [];
        }
        if (project?.repos) {
            for (const repo of project.repos) {
                if (map[repo.type]) {
                    map[repo.type].push(repo);
                }
            }
        }
        return map;
    }, [project]);
    const totalSize = useMemo(() => {
        return project?.repos?.reduce((sum, r) => sum + r.size_bytes, 0) ?? 0;
    }, [project]);
    const handleCreateRepo = async (e) => {
        e.preventDefault();
        setCreateError('');
        try {
            await createRepo.mutateAsync({
                projectName: name,
                data: {
                    name: repoName,
                    type: repoType,
                },
            });
            toast.success(`Repository "${repoName}" created.`);
            setDialogOpen(false);
            setRepoName('');
            setActiveTab(repoType);
        }
        catch (err) {
            if (err instanceof ApiError) {
                setCreateError(err.detail);
            }
            else {
                setCreateError('Failed to create repository.');
            }
        }
    };
    const openCreateDialog = (preselectedType) => {
        if (preselectedType)
            setRepoType(preselectedType);
        setDialogOpen(true);
    };
    if (isLoading) {
        return (_jsxs("div", { className: "space-y-6", children: [_jsx(Skeleton, { className: "h-5 w-48" }), _jsx(Skeleton, { className: "h-8 w-64" }), _jsx(Skeleton, { className: "h-64 w-full" })] }));
    }
    if (!project) {
        return (_jsxs("div", { className: "text-center py-12", children: [_jsx("h2", { className: "text-lg font-semibold", children: "Project not found" }), _jsxs("p", { className: "mt-2 text-sm text-muted-foreground", children: ["The project \"", name, "\" does not exist or you lack access."] }), _jsx(Button, { className: "mt-4", render: _jsx(Link, { to: "/projects" }), children: "Back to Projects" })] }));
    }
    const activity = activityData?.items ?? [];
    return (_jsxs("div", { className: "space-y-6", children: [_jsx(Breadcrumb, { children: _jsxs(BreadcrumbList, { children: [_jsx(BreadcrumbItem, { children: _jsx(BreadcrumbLink, { render: _jsx(Link, { to: "/projects" }), children: "Projects" }) }), _jsx(BreadcrumbSeparator, {}), _jsx(BreadcrumbItem, { children: _jsx(BreadcrumbPage, { children: project.name }) })] }) }), _jsxs("div", { children: [_jsx("h1", { className: "text-[28px] font-semibold leading-tight", children: project.name }), project.description_md && (_jsx("p", { className: "mt-1 text-sm text-muted-foreground", children: project.description_md }))] }), _jsxs(Tabs, { value: activeTab, onValueChange: (val) => setActiveTab(val), children: [_jsx(TabsList, { variant: "line", className: "w-full overflow-x-auto", children: ALL_TABS.map((tab) => (_jsxs(TabsTrigger, { value: tab, children: [tab === 'overview'
                                    ? 'Overview'
                                    : REPO_TYPES.find((t) => t.value === tab)?.label ?? tab, tab !== 'overview' && reposByType[tab]?.length > 0 && (_jsxs("span", { className: "ml-1 text-xs text-muted-foreground tabular-nums", children: ["(", reposByType[tab].length, ")"] }))] }, tab))) }), _jsx(TabsContent, { value: "overview", children: _jsxs("div", { className: "mt-4 grid gap-6 lg:grid-cols-2", children: [_jsxs(Card, { children: [_jsx(CardHeader, { children: _jsxs("div", { className: "flex items-center justify-between", children: [_jsxs("div", { className: "flex items-center gap-2", children: [_jsx(Users, { className: "size-4 text-muted-foreground" }), _jsx(CardTitle, { children: "Members" })] }), _jsx(Button, { variant: "outline", size: "sm", render: _jsx(Link, { to: "/admin/users" }), children: "Add Member" })] }) }), _jsx(CardContent, { children: project.members.length === 0 ? (_jsx("p", { className: "text-sm text-muted-foreground", children: "No members." })) : (_jsx("div", { className: "space-y-2", children: project.members.map((m) => (_jsxs("div", { className: "flex items-center gap-3 text-sm", children: [_jsx(Avatar, { size: "sm", children: _jsx(AvatarFallback, { children: m.login.slice(0, 2).toUpperCase() }) }), _jsxs("div", { children: [_jsx("p", { className: "font-medium", children: m.login }), _jsx("p", { className: "text-xs text-muted-foreground", children: m.email })] })] }, m.user_id))) })) })] }), _jsxs(Card, { children: [_jsx(CardHeader, { children: _jsx(CardTitle, { children: "Storage" }) }), _jsxs(CardContent, { children: [_jsx(StorageGauge, { used: totalSize, total: Math.max(totalSize * 2, 1073741824) }), _jsxs("p", { className: "mt-3 text-xs text-muted-foreground", children: [project.repos?.length ?? 0, " repositories,", ' ', formatBytes(totalSize), " total"] })] })] }), _jsxs(Card, { className: "lg:col-span-2", children: [_jsx(CardHeader, { children: _jsxs("div", { className: "flex items-center gap-2", children: [_jsx(Activity, { className: "size-4 text-muted-foreground" }), _jsx(CardTitle, { children: "Project Activity" })] }) }), _jsx(CardContent, { children: activity.length === 0 ? (_jsx("p", { className: "text-sm text-muted-foreground", children: "No activity yet." })) : (_jsx("div", { className: "max-h-[400px] space-y-3 overflow-y-auto", children: activity.slice(0, 50).map((event) => (_jsxs("div", { className: "flex items-start gap-3 text-sm", children: [_jsx("span", { className: "shrink-0 text-xs text-muted-foreground tabular-nums", children: formatDate(event.created_at) }), _jsxs("span", { className: "flex-1", children: [event.action, ' ', _jsxs("span", { className: "text-muted-foreground", children: [event.target_kind, "/", event.target_id] })] })] }, event.id))) })) })] })] }) }), REPO_TYPES.map((rt) => (_jsx(TabsContent, { value: rt.value, children: _jsxs("div", { className: "mt-4 space-y-4", children: [_jsxs("div", { className: "flex items-center justify-between", children: [_jsxs("h2", { className: "text-lg font-semibold", children: [rt.label, " Repositories"] }), _jsxs(Button, { size: "sm", onClick: () => openCreateDialog(rt.value), children: [_jsx(Plus, { className: "mr-1.5 size-4" }), "Create Repository"] })] }), reposByType[rt.value].length === 0 ? (_jsxs("div", { className: "flex flex-col items-center justify-center rounded-lg border border-dashed p-12 text-center", children: [_jsx(FolderGit2, { className: "size-12 text-muted-foreground/50" }), _jsx("h3", { className: "mt-4 text-lg font-semibold", children: "No repositories" }), _jsxs("p", { className: "mt-2 max-w-md text-sm text-muted-foreground", children: ["Create your first ", rt.label.toLowerCase(), " repository"] }), _jsxs(Button, { className: "mt-6", onClick: () => openCreateDialog(rt.value), children: [_jsx(Plus, { className: "mr-1.5 size-4" }), "Create Repository"] })] })) : (_jsx("div", { className: "space-y-2", children: reposByType[rt.value].map((repo) => (_jsx(motion.div, { initial: { opacity: 0, y: 4 }, animate: { opacity: 1, y: 0 }, transition: { duration: 0.15 }, children: _jsx(Card, { className: "cursor-pointer transition-all duration-150 hover:-translate-y-0.5 hover:shadow-md", onClick: () => navigate(`/projects/${name}/${repo.type}/${repo.name}`), children: _jsxs(CardContent, { className: "flex items-center justify-between py-3", children: [_jsxs("div", { className: "flex items-center gap-3", children: [_jsx(TypeBadge, { type: repo.type }), _jsxs("div", { children: [_jsx("p", { className: "font-medium", children: repo.name }), repo.description_md && (_jsx("p", { className: "text-xs text-muted-foreground line-clamp-1", children: repo.description_md }))] })] }), _jsxs("div", { className: "flex items-center gap-4 text-xs text-muted-foreground", children: [_jsx("span", { children: formatBytes(repo.size_bytes) }), _jsxs("span", { children: ["Created ", formatDate(repo.created_at)] })] })] }) }) }, repo.id))) }))] }) }, rt.value)))] }), _jsx(Dialog, { open: dialogOpen, onOpenChange: setDialogOpen, children: _jsx(DialogContent, { children: _jsxs("form", { onSubmit: handleCreateRepo, children: [_jsx(DialogHeader, { children: _jsx(DialogTitle, { children: "Create Repository" }) }), _jsxs("div", { className: "space-y-4 py-4", children: [createError && (_jsx("div", { className: "rounded-md bg-destructive/10 p-3 text-sm text-destructive", children: createError })), _jsxs("div", { className: "space-y-2", children: [_jsx(Label, { htmlFor: "repo-type", children: "Type" }), _jsxs(Select, { value: repoType, onValueChange: (val) => setRepoType(val), children: [_jsx(SelectTrigger, { id: "repo-type", children: _jsx(SelectValue, {}) }), _jsx(SelectContent, { children: REPO_TYPES.map((rt) => (_jsx(SelectItem, { value: rt.value, children: rt.label }, rt.value))) })] })] }), _jsxs("div", { className: "space-y-2", children: [_jsx(Label, { htmlFor: "repo-name", children: "Repository Name" }), _jsx(Input, { id: "repo-name", value: repoName, onChange: (e) => setRepoName(e.target.value), placeholder: "my-repo", required: true, autoFocus: true })] })] }), _jsx(DialogFooter, { children: _jsx(Button, { type: "submit", disabled: createRepo.isPending || !repoName.trim(), children: createRepo.isPending ? 'Creating...' : 'Create Repository' }) })] }) }) })] }));
}
