import { jsx as _jsx, jsxs as _jsxs } from "react/jsx-runtime";
/**
 * Profile page per D-26.
 * Self-service hub: personal info, password change, API keys, S3 keys,
 * project memberships, account deletion.
 */
import { useState, useMemo, useCallback } from 'react';
import { Link } from 'react-router-dom';
import { motion } from 'framer-motion';
import { User, KeyRound, Database, FolderKanban, Trash2, RefreshCw, } from 'lucide-react';
import { toast } from 'sonner';
import { createAvatar } from '@dicebear/core';
import { initials } from '@dicebear/collection';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { Separator } from '@/components/ui/separator';
import { Skeleton } from '@/components/ui/skeleton';
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription, DialogFooter, } from '@/components/ui/dialog';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue, } from '@/components/ui/select';
import { DataTable } from '@/components/common/DataTable';
import { OneTimeReveal } from '@/components/common/OneTimeReveal';
import { useAuth } from '@/hooks/useAuth';
import { useMe, useUpdateMe, useProjects, useAPIKeys, useCreateAPIKey, useRevokeAPIKey, useS3Keys, useCreateS3Key, useRevokeS3Key, useDeleteAccount, } from '@/api/queries';
import { formatDate } from '@/lib/format';
const sectionVariants = {
    hidden: { opacity: 0, y: 12 },
    visible: (i) => ({
        opacity: 1,
        y: 0,
        transition: { delay: i * 0.05, duration: 0.2, ease: 'easeOut' },
    }),
};
function DicebearAvatar({ seed, size = 64 }) {
    const dataUri = useMemo(() => {
        const svg = createAvatar(initials, { seed, size }).toString();
        return `data:image/svg+xml;utf8,${encodeURIComponent(svg)}`;
    }, [seed, size]);
    return (_jsx("img", { src: dataUri, alt: "Avatar", className: "rounded-full", width: size, height: size }));
}
// ---------- Personal Info Section ----------
function PersonalInfoSection() {
    const { data: me, isLoading } = useMe();
    const updateMe = useUpdateMe();
    const [email, setEmail] = useState('');
    const [avatarSeed, setAvatarSeed] = useState('');
    const [initialized, setInitialized] = useState(false);
    // Initialize form from fetched data
    if (me && !initialized) {
        setEmail(me.email || '');
        setAvatarSeed(me.avatar_seed || me.login);
        setInitialized(true);
    }
    const handleRegenerate = useCallback(() => {
        const newSeed = crypto.randomUUID();
        setAvatarSeed(newSeed);
    }, []);
    const handleSave = useCallback(async () => {
        try {
            await updateMe.mutateAsync({ email, avatar_seed: avatarSeed });
            toast.success('Profile updated.');
        }
        catch {
            toast.error('Failed to update profile.');
        }
    }, [updateMe, email, avatarSeed]);
    if (isLoading) {
        return (_jsxs(Card, { children: [_jsx(CardHeader, { children: _jsxs(CardTitle, { className: "flex items-center gap-2", children: [_jsx(User, { className: "size-4" }), "Personal Information"] }) }), _jsxs(CardContent, { className: "space-y-4", children: [_jsx(Skeleton, { className: "h-16 w-16 rounded-full" }), _jsx(Skeleton, { className: "h-8 w-full max-w-sm" })] })] }));
    }
    return (_jsxs(Card, { children: [_jsx(CardHeader, { children: _jsxs(CardTitle, { className: "flex items-center gap-2", children: [_jsx(User, { className: "size-4" }), "Personal Information"] }) }), _jsxs(CardContent, { className: "space-y-4", children: [_jsxs("div", { className: "flex items-center gap-4", children: [_jsx(DicebearAvatar, { seed: avatarSeed, size: 64 }), _jsxs(Button, { variant: "outline", size: "sm", onClick: handleRegenerate, children: [_jsx(RefreshCw, { className: "mr-1.5 size-3.5" }), "Regenerate"] })] }), _jsxs("div", { className: "space-y-1.5 max-w-sm", children: [_jsx(Label, { htmlFor: "profile-login", children: "Login" }), _jsx(Input, { id: "profile-login", value: me?.login ?? '', disabled: true, className: "opacity-60" })] }), _jsxs("div", { className: "space-y-1.5 max-w-sm", children: [_jsx(Label, { htmlFor: "profile-email", children: "Email" }), _jsx(Input, { id: "profile-email", type: "email", value: email, onChange: (e) => setEmail(e.target.value) })] }), _jsx(Button, { onClick: handleSave, disabled: updateMe.isPending, children: "Save Changes" })] })] }));
}
// ---------- Change Password Section ----------
function ChangePasswordSection() {
    const { changePassword } = useAuth();
    const [current, setCurrent] = useState('');
    const [newPw, setNewPw] = useState('');
    const [confirm, setConfirm] = useState('');
    const mismatch = newPw !== confirm && confirm.length > 0;
    const tooShort = newPw.length > 0 && newPw.length < 8;
    const canSubmit = current.length > 0 && newPw.length >= 8 && newPw === confirm;
    const handleSubmit = useCallback(async () => {
        try {
            await changePassword.mutateAsync({ current, new_password: newPw });
            toast.success('Password updated.');
            setCurrent('');
            setNewPw('');
            setConfirm('');
        }
        catch {
            toast.error('Failed to change password. Check your current password.');
        }
    }, [changePassword, current, newPw]);
    return (_jsxs(Card, { children: [_jsx(CardHeader, { children: _jsxs(CardTitle, { className: "flex items-center gap-2", children: [_jsx(KeyRound, { className: "size-4" }), "Change Password"] }) }), _jsxs(CardContent, { className: "space-y-4 max-w-sm", children: [_jsxs("div", { className: "space-y-1.5", children: [_jsx(Label, { htmlFor: "current-pw", children: "Current Password" }), _jsx(Input, { id: "current-pw", type: "password", value: current, onChange: (e) => setCurrent(e.target.value) })] }), _jsxs("div", { className: "space-y-1.5", children: [_jsx(Label, { htmlFor: "new-pw", children: "New Password" }), _jsx(Input, { id: "new-pw", type: "password", value: newPw, onChange: (e) => setNewPw(e.target.value) }), tooShort && (_jsx("p", { className: "text-xs text-destructive", children: "Minimum 8 characters." }))] }), _jsxs("div", { className: "space-y-1.5", children: [_jsx(Label, { htmlFor: "confirm-pw", children: "Confirm New Password" }), _jsx(Input, { id: "confirm-pw", type: "password", value: confirm, onChange: (e) => setConfirm(e.target.value) }), mismatch && (_jsx("p", { className: "text-xs text-destructive", children: "Passwords do not match." }))] }), _jsx(Button, { onClick: handleSubmit, disabled: !canSubmit || changePassword.isPending, children: "Update Password" })] })] }));
}
// ---------- API Keys Section ----------
function APIKeysSection() {
    const { data, isLoading } = useAPIKeys();
    const createKey = useCreateAPIKey();
    const revokeKey = useRevokeAPIKey();
    const [showCreate, setShowCreate] = useState(false);
    const [keyLabel, setKeyLabel] = useState('');
    const [revealSecret, setRevealSecret] = useState('');
    const [showReveal, setShowReveal] = useState(false);
    const [revokeTarget, setRevokeTarget] = useState(null);
    const keys = data?.items ?? [];
    const handleCreate = useCallback(async () => {
        try {
            const result = await createKey.mutateAsync({ label: keyLabel });
            setShowCreate(false);
            setKeyLabel('');
            setRevealSecret(result.secret);
            setShowReveal(true);
        }
        catch {
            toast.error('Failed to create API key.');
        }
    }, [createKey, keyLabel]);
    const handleRevoke = useCallback(async () => {
        if (!revokeTarget)
            return;
        try {
            await revokeKey.mutateAsync(revokeTarget.id);
            toast.success('API key revoked.');
            setRevokeTarget(null);
        }
        catch {
            toast.error('Failed to revoke API key.');
        }
    }, [revokeKey, revokeTarget]);
    const columns = [
        { id: 'label', name: 'Name', render: (row) => row.label },
        {
            id: 'prefix',
            name: 'Key Prefix',
            render: (row) => _jsxs("code", { className: "font-mono text-xs", children: [row.prefix, "..."] }),
        },
        { id: 'created_at', name: 'Created', render: (row) => formatDate(row.created_at) },
        {
            id: 'last_used_at',
            name: 'Last Used',
            render: (row) => (row.last_used_at ? formatDate(row.last_used_at) : 'Never'),
        },
        {
            id: 'actions',
            name: '',
            className: 'w-20',
            render: (row) => (_jsx(Button, { variant: "ghost", size: "sm", className: "text-destructive hover:text-destructive", onClick: () => setRevokeTarget(row), children: "Revoke" })),
        },
    ];
    return (_jsxs(Card, { children: [_jsxs(CardHeader, { className: "flex flex-row items-center justify-between", children: [_jsxs(CardTitle, { className: "flex items-center gap-2", children: [_jsx(KeyRound, { className: "size-4" }), "API Keys"] }), _jsx(Button, { size: "sm", onClick: () => setShowCreate(true), children: "Create API Key" })] }), _jsx(CardContent, { children: _jsx(DataTable, { columns: columns, data: keys, loading: isLoading, emptyMessage: "No API keys yet." }) }), _jsx(Dialog, { open: showCreate, onOpenChange: setShowCreate, children: _jsxs(DialogContent, { children: [_jsxs(DialogHeader, { children: [_jsx(DialogTitle, { children: "Create API Key" }), _jsx(DialogDescription, { children: "Enter a name to identify this key." })] }), _jsx("div", { className: "space-y-3", children: _jsxs("div", { className: "space-y-1.5", children: [_jsx(Label, { htmlFor: "api-key-label", children: "Name" }), _jsx(Input, { id: "api-key-label", value: keyLabel, onChange: (e) => setKeyLabel(e.target.value), placeholder: "e.g., CI Pipeline" })] }) }), _jsxs(DialogFooter, { children: [_jsx(Button, { variant: "outline", onClick: () => setShowCreate(false), children: "Cancel" }), _jsx(Button, { onClick: handleCreate, disabled: !keyLabel.trim() || createKey.isPending, children: "Create" })] })] }) }), _jsx(OneTimeReveal, { open: showReveal, onOpenChange: setShowReveal, title: "Your API Key", secret: revealSecret, warningText: "This key will not be shown again. Copy it now." }), _jsx(Dialog, { open: !!revokeTarget, onOpenChange: (open) => !open && setRevokeTarget(null), children: _jsxs(DialogContent, { children: [_jsxs(DialogHeader, { children: [_jsx(DialogTitle, { children: "Revoke API Key" }), _jsx(DialogDescription, { children: "This key will stop working immediately. Continue?" })] }), _jsxs(DialogFooter, { children: [_jsx(Button, { variant: "outline", onClick: () => setRevokeTarget(null), children: "Cancel" }), _jsx(Button, { variant: "destructive", onClick: handleRevoke, disabled: revokeKey.isPending, children: "Revoke" })] })] }) })] }));
}
// ---------- S3 Keys Section ----------
function S3KeysSection() {
    const { data, isLoading } = useS3Keys();
    const { data: projectsData } = useProjects();
    const createKey = useCreateS3Key();
    const revokeKey = useRevokeS3Key();
    const [showCreate, setShowCreate] = useState(false);
    const [selectedProject, setSelectedProject] = useState('');
    const [revealSecret, setRevealSecret] = useState('');
    const [showReveal, setShowReveal] = useState(false);
    const [revokeTarget, setRevokeTarget] = useState(null);
    const keys = data?.items ?? [];
    const projects = projectsData?.items ?? [];
    const handleCreate = useCallback(async () => {
        const projectId = parseInt(selectedProject, 10);
        if (!projectId)
            return;
        try {
            const result = await createKey.mutateAsync({ project_id: projectId });
            setShowCreate(false);
            setSelectedProject('');
            setRevealSecret(result.secret_access_key);
            setShowReveal(true);
        }
        catch {
            toast.error('Failed to create S3 key.');
        }
    }, [createKey, selectedProject]);
    const handleRevoke = useCallback(async () => {
        if (!revokeTarget)
            return;
        try {
            await revokeKey.mutateAsync(revokeTarget.id);
            toast.success('S3 key revoked.');
            setRevokeTarget(null);
        }
        catch {
            toast.error('Failed to revoke S3 key.');
        }
    }, [revokeKey, revokeTarget]);
    // Map project IDs to names for display
    const projectMap = useMemo(() => {
        const map = new Map();
        projects.forEach((p) => map.set(p.id, p.name));
        return map;
    }, [projects]);
    const columns = [
        {
            id: 'access_key_id',
            name: 'Access Key ID',
            render: (row) => _jsx("code", { className: "font-mono text-xs", children: row.access_key_id }),
        },
        {
            id: 'project',
            name: 'Project',
            render: (row) => projectMap.get(row.project_id) ?? `Project #${row.project_id}`,
        },
        { id: 'created_at', name: 'Created', render: (row) => formatDate(row.created_at) },
        {
            id: 'actions',
            name: '',
            className: 'w-20',
            render: (row) => (_jsx(Button, { variant: "ghost", size: "sm", className: "text-destructive hover:text-destructive", onClick: () => setRevokeTarget(row), children: "Revoke" })),
        },
    ];
    return (_jsxs(Card, { children: [_jsxs(CardHeader, { className: "flex flex-row items-center justify-between", children: [_jsxs(CardTitle, { className: "flex items-center gap-2", children: [_jsx(Database, { className: "size-4" }), "S3 Access Keys"] }), _jsx(Button, { size: "sm", onClick: () => setShowCreate(true), children: "Create S3 Key" })] }), _jsx(CardContent, { children: _jsx(DataTable, { columns: columns, data: keys, loading: isLoading, emptyMessage: "No S3 access keys yet." }) }), _jsx(Dialog, { open: showCreate, onOpenChange: setShowCreate, children: _jsxs(DialogContent, { children: [_jsxs(DialogHeader, { children: [_jsx(DialogTitle, { children: "Create S3 Key" }), _jsx(DialogDescription, { children: "Select a project to create an S3 access key for." })] }), _jsx("div", { className: "space-y-3", children: _jsxs("div", { className: "space-y-1.5", children: [_jsx(Label, { children: "Project" }), _jsxs(Select, { value: selectedProject, onValueChange: (val) => setSelectedProject(val ?? ''), children: [_jsx(SelectTrigger, { className: "w-full", children: _jsx(SelectValue, { placeholder: "Select project" }) }), _jsx(SelectContent, { children: projects.map((p) => (_jsx(SelectItem, { value: String(p.id), children: p.name }, p.id))) })] })] }) }), _jsxs(DialogFooter, { children: [_jsx(Button, { variant: "outline", onClick: () => setShowCreate(false), children: "Cancel" }), _jsx(Button, { onClick: handleCreate, disabled: !selectedProject || createKey.isPending, children: "Create" })] })] }) }), _jsx(OneTimeReveal, { open: showReveal, onOpenChange: setShowReveal, title: "Your S3 Secret", secret: revealSecret, warningText: "This secret will not be shown again. Copy it now." }), _jsx(Dialog, { open: !!revokeTarget, onOpenChange: (open) => !open && setRevokeTarget(null), children: _jsxs(DialogContent, { children: [_jsxs(DialogHeader, { children: [_jsx(DialogTitle, { children: "Revoke S3 Key" }), _jsx(DialogDescription, { children: "This key will stop working immediately. Continue?" })] }), _jsxs(DialogFooter, { children: [_jsx(Button, { variant: "outline", onClick: () => setRevokeTarget(null), children: "Cancel" }), _jsx(Button, { variant: "destructive", onClick: handleRevoke, disabled: revokeKey.isPending, children: "Revoke" })] })] }) })] }));
}
// ---------- My Projects Section ----------
function MyProjectsSection() {
    const { data, isLoading } = useProjects();
    const projects = data?.items ?? [];
    return (_jsxs(Card, { children: [_jsx(CardHeader, { children: _jsxs(CardTitle, { className: "flex items-center gap-2", children: [_jsx(FolderKanban, { className: "size-4" }), "My Projects"] }) }), _jsx(CardContent, { children: isLoading ? (_jsx("div", { className: "space-y-2", children: Array.from({ length: 3 }).map((_, i) => (_jsx(Skeleton, { className: "h-8 w-full" }, i))) })) : projects.length === 0 ? (_jsx("p", { className: "text-sm text-muted-foreground", children: "You are not a member of any projects yet." })) : (_jsx("div", { className: "space-y-1", children: projects.map((p) => (_jsxs(Button, { variant: "ghost", className: "w-full justify-start", render: _jsx(Link, { to: `/projects/${p.name}` }), children: [_jsx(FolderKanban, { className: "mr-2 size-4" }), p.name] }, p.id))) })) })] }));
}
// ---------- Delete Account Section ----------
function DeleteAccountSection() {
    const { user, logout } = useAuth();
    const deleteAccount = useDeleteAccount();
    const [showConfirm, setShowConfirm] = useState(false);
    const [confirmText, setConfirmText] = useState('');
    const loginMatch = confirmText === (user?.login ?? '');
    const handleDelete = useCallback(async () => {
        try {
            await deleteAccount.mutateAsync();
            toast.success('Account deleted.');
            logout.mutate();
        }
        catch {
            toast.error('Failed to delete account.');
        }
    }, [deleteAccount, logout]);
    return (_jsxs(Card, { className: "border-destructive/30", children: [_jsx(CardHeader, { children: _jsxs(CardTitle, { className: "flex items-center gap-2 text-destructive", children: [_jsx(Trash2, { className: "size-4" }), "Delete Account"] }) }), _jsxs(CardContent, { children: [_jsx("p", { className: "text-sm text-muted-foreground mb-4", children: "Permanently remove your account and all personal API keys. This action cannot be undone." }), _jsx(Button, { variant: "destructive", onClick: () => setShowConfirm(true), children: "Delete Account" })] }), _jsx(Dialog, { open: showConfirm, onOpenChange: (open) => { setShowConfirm(open); if (!open)
                    setConfirmText(''); }, children: _jsxs(DialogContent, { children: [_jsxs(DialogHeader, { children: [_jsx(DialogTitle, { children: "Delete Account" }), _jsx(DialogDescription, { children: "This will permanently remove your account and all personal API keys. You will be logged out immediately. Type your login to confirm." })] }), _jsxs("div", { className: "space-y-1.5", children: [_jsxs(Label, { htmlFor: "delete-confirm", children: ["Type ", _jsx("code", { className: "font-mono text-sm font-semibold", children: user?.login }), " to confirm"] }), _jsx(Input, { id: "delete-confirm", value: confirmText, onChange: (e) => setConfirmText(e.target.value), placeholder: user?.login })] }), _jsxs(DialogFooter, { children: [_jsx(Button, { variant: "outline", onClick: () => setShowConfirm(false), children: "Cancel" }), _jsx(Button, { variant: "destructive", onClick: handleDelete, disabled: !loginMatch || deleteAccount.isPending, children: "Delete Account" })] })] }) })] }));
}
// ---------- Main Profile Page ----------
export function ProfilePage() {
    return (_jsxs("div", { className: "space-y-6 max-w-3xl", children: [_jsx("h1", { className: "text-[28px] font-semibold leading-tight", children: "Profile" }), _jsx(motion.div, { custom: 0, variants: sectionVariants, initial: "hidden", animate: "visible", children: _jsx(PersonalInfoSection, {}) }), _jsx(motion.div, { custom: 1, variants: sectionVariants, initial: "hidden", animate: "visible", children: _jsx(ChangePasswordSection, {}) }), _jsx(motion.div, { custom: 2, variants: sectionVariants, initial: "hidden", animate: "visible", children: _jsx(APIKeysSection, {}) }), _jsx(motion.div, { custom: 3, variants: sectionVariants, initial: "hidden", animate: "visible", children: _jsx(S3KeysSection, {}) }), _jsx(motion.div, { custom: 4, variants: sectionVariants, initial: "hidden", animate: "visible", children: _jsx(MyProjectsSection, {}) }), _jsx(Separator, {}), _jsx(motion.div, { custom: 5, variants: sectionVariants, initial: "hidden", animate: "visible", children: _jsx(DeleteAccountSection, {}) })] }));
}
