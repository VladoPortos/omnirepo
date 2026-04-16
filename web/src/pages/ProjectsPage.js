import { jsx as _jsx, jsxs as _jsxs } from "react/jsx-runtime";
/**
 * Projects list page per D-04.
 * Project cards with member/repo counts, empty state, create dialog.
 */
import { useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { motion } from 'framer-motion';
import { Plus, FolderGit2, Users } from 'lucide-react';
import { toast } from 'sonner';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { Skeleton } from '@/components/ui/skeleton';
import { Textarea } from '@/components/ui/textarea';
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogFooter, DialogTrigger, } from '@/components/ui/dialog';
import { useProjects, useCreateProject } from '@/api/queries';
import { formatBytes, formatDate } from '@/lib/format';
import { ApiError } from '@/api/client';
const cardVariants = {
    hidden: { opacity: 0, y: 12 },
    visible: (i) => ({
        opacity: 1,
        y: 0,
        transition: { delay: i * 0.04, duration: 0.2, ease: 'easeOut' },
    }),
};
export function ProjectsPage() {
    const navigate = useNavigate();
    const { data, isLoading } = useProjects();
    const createProject = useCreateProject();
    const [dialogOpen, setDialogOpen] = useState(false);
    const [name, setName] = useState('');
    const [description, setDescription] = useState('');
    const [error, setError] = useState('');
    const projects = data?.items ?? [];
    const handleCreate = async (e) => {
        e.preventDefault();
        setError('');
        try {
            const result = await createProject.mutateAsync({
                name,
                description_md: description || undefined,
            });
            toast.success(`Project "${result.name}" created.`);
            setDialogOpen(false);
            setName('');
            setDescription('');
            navigate(`/projects/${result.name}`);
        }
        catch (err) {
            if (err instanceof ApiError) {
                setError(err.detail);
            }
            else {
                setError('Failed to create project.');
            }
        }
    };
    return (_jsxs("div", { className: "space-y-6", children: [_jsxs("div", { className: "flex items-center justify-between", children: [_jsx("h1", { className: "text-[28px] font-semibold leading-tight", children: "Projects" }), _jsxs(Dialog, { open: dialogOpen, onOpenChange: setDialogOpen, children: [_jsxs(DialogTrigger, { render: _jsx(Button, { size: "sm" }), children: [_jsx(Plus, { className: "mr-1.5 size-4" }), "Create Project"] }), _jsx(DialogContent, { children: _jsxs("form", { onSubmit: handleCreate, children: [_jsx(DialogHeader, { children: _jsx(DialogTitle, { children: "Create Project" }) }), _jsxs("div", { className: "space-y-4 py-4", children: [error && (_jsx("div", { className: "rounded-md bg-destructive/10 p-3 text-sm text-destructive", children: error })), _jsxs("div", { className: "space-y-2", children: [_jsx(Label, { htmlFor: "project-name", children: "Project Name" }), _jsx(Input, { id: "project-name", value: name, onChange: (e) => setName(e.target.value), placeholder: "my-project", required: true, autoFocus: true }), _jsx("p", { className: "text-xs text-muted-foreground", children: "URL-safe slug (lowercase letters, numbers, hyphens)" })] }), _jsxs("div", { className: "space-y-2", children: [_jsx(Label, { htmlFor: "project-desc", children: "Description (optional)" }), _jsx(Textarea, { id: "project-desc", value: description, onChange: (e) => setDescription(e.target.value), placeholder: "Brief description of this project...", rows: 3 })] })] }), _jsx(DialogFooter, { children: _jsx(Button, { type: "submit", disabled: createProject.isPending || !name.trim(), children: createProject.isPending ? 'Creating...' : 'Create Project' }) })] }) })] })] }), isLoading ? (_jsx("div", { className: "grid gap-4 md:grid-cols-2 xl:grid-cols-3", children: Array.from({ length: 6 }).map((_, i) => (_jsxs(Card, { children: [_jsx(CardHeader, { children: _jsx(Skeleton, { className: "h-5 w-32" }) }), _jsxs(CardContent, { children: [_jsx(Skeleton, { className: "h-4 w-full" }), _jsx(Skeleton, { className: "mt-2 h-4 w-24" })] })] }, i))) })) : projects.length === 0 ? (_jsxs("div", { className: "flex flex-col items-center justify-center rounded-lg border border-dashed p-12 text-center", children: [_jsx(FolderGit2, { className: "size-12 text-muted-foreground/50" }), _jsx("h2", { className: "mt-4 text-lg font-semibold", children: "No projects yet" }), _jsx("p", { className: "mt-2 max-w-md text-sm text-muted-foreground", children: "Create your first project to start hosting artifacts." }), _jsxs(Button, { className: "mt-6", onClick: () => setDialogOpen(true), children: [_jsx(Plus, { className: "mr-1.5 size-4" }), "Create Project"] })] })) : (_jsx("div", { className: "grid gap-4 md:grid-cols-2 xl:grid-cols-3", children: projects.map((project, i) => (_jsx(motion.div, { custom: i, initial: "hidden", animate: "visible", variants: cardVariants, children: _jsxs(Card, { className: "cursor-pointer transition-all duration-150 hover:-translate-y-0.5 hover:shadow-md", onClick: () => navigate(`/projects/${project.name}`), children: [_jsx(CardHeader, { children: _jsx(CardTitle, { className: "text-base", children: project.name }) }), _jsxs(CardContent, { children: [project.description_md && (_jsx("p", { className: "mb-3 line-clamp-2 text-sm text-muted-foreground", children: project.description_md })), _jsxs("div", { className: "flex items-center gap-4 text-xs text-muted-foreground", children: [_jsxs("span", { className: "inline-flex items-center gap-1", children: [_jsx(Users, { className: "size-3.5" }), project.member_count, " member", project.member_count !== 1 ? 's' : ''] }), _jsxs("span", { className: "inline-flex items-center gap-1", children: [_jsx(FolderGit2, { className: "size-3.5" }), project.repo_count, " repo", project.repo_count !== 1 ? 's' : ''] }), _jsx("span", { children: formatBytes(project.size_bytes) })] }), _jsxs("p", { className: "mt-2 text-xs text-muted-foreground", children: ["Created ", formatDate(project.created_at)] })] })] }) }, project.id))) }))] }));
}
