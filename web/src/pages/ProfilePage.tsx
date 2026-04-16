/**
 * Profile page per D-26.
 * Self-service hub: personal info, password change, API keys, S3 keys,
 * project memberships, account deletion.
 */

import { useState, useMemo, useCallback } from 'react';
import { Link } from 'react-router-dom';
import { motion } from 'framer-motion';
import {
  User,
  KeyRound,
  Database,
  FolderKanban,
  Trash2,
  RefreshCw,
} from 'lucide-react';
import { toast } from 'sonner';
import { createAvatar } from '@dicebear/core';
import { initials } from '@dicebear/collection';

import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { Separator } from '@/components/ui/separator';
import { Skeleton } from '@/components/ui/skeleton';
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
  DialogFooter,
} from '@/components/ui/dialog';
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select';
import { DataTable, type ColumnDef } from '@/components/common/DataTable';
import { OneTimeReveal } from '@/components/common/OneTimeReveal';
import { useAuth } from '@/hooks/useAuth';
import {
  useMe,
  useUpdateMe,
  useProjects,
  useAPIKeys,
  useCreateAPIKey,
  useRevokeAPIKey,
  useS3Keys,
  useCreateS3Key,
  useRevokeS3Key,
  useDeleteAccount,
} from '@/api/queries';
import type { APIKey, S3Key } from '@/api/types';
import { formatDate } from '@/lib/format';

const sectionVariants = {
  hidden: { opacity: 0, y: 12 },
  visible: (i: number) => ({
    opacity: 1,
    y: 0,
    transition: { delay: i * 0.05, duration: 0.2, ease: 'easeOut' as const },
  }),
};

function DicebearAvatar({ seed, size = 64 }: { seed: string; size?: number }) {
  const dataUri = useMemo(() => {
    const svg = createAvatar(initials, { seed, size }).toString();
    return `data:image/svg+xml;utf8,${encodeURIComponent(svg)}`;
  }, [seed, size]);

  return (
    <img
      src={dataUri}
      alt="Avatar"
      className="rounded-full"
      width={size}
      height={size}
    />
  );
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
    } catch {
      toast.error('Failed to update profile.');
    }
  }, [updateMe, email, avatarSeed]);

  if (isLoading) {
    return (
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <User className="size-4" />
            Personal Information
          </CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          <Skeleton className="h-16 w-16 rounded-full" />
          <Skeleton className="h-8 w-full max-w-sm" />
        </CardContent>
      </Card>
    );
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <User className="size-4" />
          Personal Information
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-4">
        <div className="flex items-center gap-4">
          <DicebearAvatar seed={avatarSeed} size={64} />
          <Button variant="outline" size="sm" onClick={handleRegenerate}>
            <RefreshCw className="mr-1.5 size-3.5" />
            Regenerate
          </Button>
        </div>

        <div className="space-y-1.5 max-w-sm">
          <Label htmlFor="profile-login">Login</Label>
          <Input
            id="profile-login"
            value={me?.login ?? ''}
            disabled
            className="opacity-60"
          />
        </div>

        <div className="space-y-1.5 max-w-sm">
          <Label htmlFor="profile-email">Email</Label>
          <Input
            id="profile-email"
            type="email"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
          />
        </div>

        <Button
          onClick={handleSave}
          disabled={updateMe.isPending}
        >
          Save Changes
        </Button>
      </CardContent>
    </Card>
  );
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
    } catch {
      toast.error('Failed to change password. Check your current password.');
    }
  }, [changePassword, current, newPw]);

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <KeyRound className="size-4" />
          Change Password
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-4 max-w-sm">
        <div className="space-y-1.5">
          <Label htmlFor="current-pw">Current Password</Label>
          <Input
            id="current-pw"
            type="password"
            value={current}
            onChange={(e) => setCurrent(e.target.value)}
          />
        </div>
        <div className="space-y-1.5">
          <Label htmlFor="new-pw">New Password</Label>
          <Input
            id="new-pw"
            type="password"
            value={newPw}
            onChange={(e) => setNewPw(e.target.value)}
          />
          {tooShort && (
            <p className="text-xs text-destructive">Minimum 8 characters.</p>
          )}
        </div>
        <div className="space-y-1.5">
          <Label htmlFor="confirm-pw">Confirm New Password</Label>
          <Input
            id="confirm-pw"
            type="password"
            value={confirm}
            onChange={(e) => setConfirm(e.target.value)}
          />
          {mismatch && (
            <p className="text-xs text-destructive">Passwords do not match.</p>
          )}
        </div>
        <Button
          onClick={handleSubmit}
          disabled={!canSubmit || changePassword.isPending}
        >
          Update Password
        </Button>
      </CardContent>
    </Card>
  );
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
  const [revokeTarget, setRevokeTarget] = useState<APIKey | null>(null);

  const keys = data ?? [];

  const handleCreate = useCallback(async () => {
    try {
      const result = await createKey.mutateAsync({ name: keyLabel });
      setShowCreate(false);
      setKeyLabel('');
      setRevealSecret(result.secret);
      setShowReveal(true);
    } catch {
      toast.error('Failed to create API key.');
    }
  }, [createKey, keyLabel]);

  const handleRevoke = useCallback(async () => {
    if (!revokeTarget) return;
    try {
      await revokeKey.mutateAsync(revokeTarget.id);
      toast.success('API key revoked.');
      setRevokeTarget(null);
    } catch {
      toast.error('Failed to revoke API key.');
    }
  }, [revokeKey, revokeTarget]);

  const columns: ColumnDef<APIKey>[] = [
    { id: 'name', name: 'Name', render: (row) => row.name },
    {
      id: 'prefix',
      name: 'Key Prefix',
      render: (row) => <code className="font-mono text-xs">{row.prefix}...</code>,
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
      render: (row) => (
        <Button
          variant="ghost"
          size="sm"
          className="text-destructive hover:text-destructive"
          onClick={() => setRevokeTarget(row)}
        >
          Revoke
        </Button>
      ),
    },
  ];

  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between">
        <CardTitle className="flex items-center gap-2">
          <KeyRound className="size-4" />
          API Keys
        </CardTitle>
        <Button size="sm" onClick={() => setShowCreate(true)}>
          Create API Key
        </Button>
      </CardHeader>
      <CardContent>
        <DataTable
          columns={columns}
          data={keys}
          loading={isLoading}
          emptyMessage="No API keys yet."
        />
      </CardContent>

      {/* Create dialog */}
      <Dialog open={showCreate} onOpenChange={setShowCreate}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Create API Key</DialogTitle>
            <DialogDescription>
              Enter a name to identify this key.
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-3">
            <div className="space-y-1.5">
              <Label htmlFor="api-key-label">Name</Label>
              <Input
                id="api-key-label"
                value={keyLabel}
                onChange={(e) => setKeyLabel(e.target.value)}
                placeholder="e.g., CI Pipeline"
              />
            </div>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setShowCreate(false)}>
              Cancel
            </Button>
            <Button
              onClick={handleCreate}
              disabled={!keyLabel.trim() || createKey.isPending}
            >
              Create
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* One-time reveal */}
      <OneTimeReveal
        open={showReveal}
        onOpenChange={setShowReveal}
        title="Your API Key"
        secret={revealSecret}
        warningText="This key will not be shown again. Copy it now."
      />

      {/* Revoke confirmation */}
      <Dialog open={!!revokeTarget} onOpenChange={(open) => !open && setRevokeTarget(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Revoke API Key</DialogTitle>
            <DialogDescription>
              This key will stop working immediately. Continue?
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setRevokeTarget(null)}>
              Cancel
            </Button>
            <Button
              variant="destructive"
              onClick={handleRevoke}
              disabled={revokeKey.isPending}
            >
              Revoke
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </Card>
  );
}

// ---------- S3 Keys Section ----------

function S3KeysSection() {
  const { data, isLoading } = useS3Keys();
  const { data: projectsData } = useProjects();
  const createKey = useCreateS3Key();
  const revokeKey = useRevokeS3Key();
  const [showCreate, setShowCreate] = useState(false);
  const [selectedProject, setSelectedProject] = useState<string>('');
  const [revealSecret, setRevealSecret] = useState('');
  const [showReveal, setShowReveal] = useState(false);
  const [revokeTarget, setRevokeTarget] = useState<S3Key | null>(null);

  const keys = data?.items ?? [];
  const projects = useMemo(() => projectsData?.items ?? [], [projectsData?.items]);

  const handleCreate = useCallback(async () => {
    const projectId = parseInt(selectedProject, 10);
    if (!projectId) return;
    try {
      const result = await createKey.mutateAsync({ project_id: projectId });
      setShowCreate(false);
      setSelectedProject('');
      setRevealSecret(result.secret_access_key);
      setShowReveal(true);
    } catch {
      toast.error('Failed to create S3 key.');
    }
  }, [createKey, selectedProject]);

  const handleRevoke = useCallback(async () => {
    if (!revokeTarget) return;
    try {
      await revokeKey.mutateAsync(revokeTarget.id);
      toast.success('S3 key revoked.');
      setRevokeTarget(null);
    } catch {
      toast.error('Failed to revoke S3 key.');
    }
  }, [revokeKey, revokeTarget]);

  // Map project IDs to names for display
  const projectMap = useMemo(() => {
    const map = new Map<number, string>();
    projects.forEach((p) => map.set(p.id, p.name));
    return map;
  }, [projects]);

  const columns: ColumnDef<S3Key>[] = [
    {
      id: 'access_key_id',
      name: 'Access Key ID',
      render: (row) => <code className="font-mono text-xs">{row.access_key_id}</code>,
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
      render: (row) => (
        <Button
          variant="ghost"
          size="sm"
          className="text-destructive hover:text-destructive"
          onClick={() => setRevokeTarget(row)}
        >
          Revoke
        </Button>
      ),
    },
  ];

  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between">
        <CardTitle className="flex items-center gap-2">
          <Database className="size-4" />
          S3 Access Keys
        </CardTitle>
        <Button size="sm" onClick={() => setShowCreate(true)}>
          Create S3 Key
        </Button>
      </CardHeader>
      <CardContent>
        <DataTable
          columns={columns}
          data={keys}
          loading={isLoading}
          emptyMessage="No S3 access keys yet."
        />
      </CardContent>

      {/* Create dialog */}
      <Dialog open={showCreate} onOpenChange={setShowCreate}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Create S3 Key</DialogTitle>
            <DialogDescription>
              Select a project to create an S3 access key for.
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-3">
            <div className="space-y-1.5">
              <Label>Project</Label>
              <Select value={selectedProject} onValueChange={(val) => setSelectedProject(val ?? '')}>
                <SelectTrigger className="w-full">
                  <SelectValue placeholder="Select project" />
                </SelectTrigger>
                <SelectContent>
                  {projects.map((p) => (
                    <SelectItem key={p.id} value={String(p.id)}>
                      {p.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setShowCreate(false)}>
              Cancel
            </Button>
            <Button
              onClick={handleCreate}
              disabled={!selectedProject || createKey.isPending}
            >
              Create
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* One-time reveal */}
      <OneTimeReveal
        open={showReveal}
        onOpenChange={setShowReveal}
        title="Your S3 Secret"
        secret={revealSecret}
        warningText="This secret will not be shown again. Copy it now."
      />

      {/* Revoke confirmation */}
      <Dialog open={!!revokeTarget} onOpenChange={(open) => !open && setRevokeTarget(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Revoke S3 Key</DialogTitle>
            <DialogDescription>
              This key will stop working immediately. Continue?
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setRevokeTarget(null)}>
              Cancel
            </Button>
            <Button
              variant="destructive"
              onClick={handleRevoke}
              disabled={revokeKey.isPending}
            >
              Revoke
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </Card>
  );
}

// ---------- My Projects Section ----------

function MyProjectsSection() {
  const { data, isLoading } = useProjects();
  const projects = data?.items ?? [];

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <FolderKanban className="size-4" />
          My Projects
        </CardTitle>
      </CardHeader>
      <CardContent>
        {isLoading ? (
          <div className="space-y-2">
            {Array.from({ length: 3 }).map((_, i) => (
              <Skeleton key={i} className="h-8 w-full" />
            ))}
          </div>
        ) : projects.length === 0 ? (
          <p className="text-sm text-muted-foreground">
            You are not a member of any projects yet.
          </p>
        ) : (
          <div className="space-y-1">
            {projects.map((p) => (
              <Button
                key={p.id}
                variant="ghost"
                className="w-full justify-start"
                nativeButton={false}
                render={<Link to={`/projects/${p.name}`} />}
              >
                <FolderKanban className="mr-2 size-4" />
                {p.name}
              </Button>
            ))}
          </div>
        )}
      </CardContent>
    </Card>
  );
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
    } catch {
      toast.error('Failed to delete account.');
    }
  }, [deleteAccount, logout]);

  return (
    <Card className="border-destructive/30">
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-destructive">
          <Trash2 className="size-4" />
          Delete Account
        </CardTitle>
      </CardHeader>
      <CardContent>
        <p className="text-sm text-muted-foreground mb-4">
          Permanently remove your account and all personal API keys. This action
          cannot be undone.
        </p>
        <Button
          variant="destructive"
          onClick={() => setShowConfirm(true)}
        >
          Delete Account
        </Button>
      </CardContent>

      <Dialog open={showConfirm} onOpenChange={(open) => { setShowConfirm(open); if (!open) setConfirmText(''); }}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete Account</DialogTitle>
            <DialogDescription>
              This will permanently remove your account and all personal API keys.
              You will be logged out immediately. Type your login to confirm.
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-1.5">
            <Label htmlFor="delete-confirm">
              Type <code className="font-mono text-sm font-semibold">{user?.login}</code> to confirm
            </Label>
            <Input
              id="delete-confirm"
              value={confirmText}
              onChange={(e) => setConfirmText(e.target.value)}
              placeholder={user?.login}
            />
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setShowConfirm(false)}>
              Cancel
            </Button>
            <Button
              variant="destructive"
              onClick={handleDelete}
              disabled={!loginMatch || deleteAccount.isPending}
            >
              Delete Account
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </Card>
  );
}

// ---------- Main Profile Page ----------

export function ProfilePage() {
  return (
    <div className="space-y-6 max-w-3xl">
      <h1 className="text-[28px] font-semibold leading-tight">Profile</h1>

      <motion.div custom={0} variants={sectionVariants} initial="hidden" animate="visible">
        <PersonalInfoSection />
      </motion.div>

      <motion.div custom={1} variants={sectionVariants} initial="hidden" animate="visible">
        <ChangePasswordSection />
      </motion.div>

      <motion.div custom={2} variants={sectionVariants} initial="hidden" animate="visible">
        <APIKeysSection />
      </motion.div>

      <motion.div custom={3} variants={sectionVariants} initial="hidden" animate="visible">
        <S3KeysSection />
      </motion.div>

      <motion.div custom={4} variants={sectionVariants} initial="hidden" animate="visible">
        <MyProjectsSection />
      </motion.div>

      <Separator />

      <motion.div custom={5} variants={sectionVariants} initial="hidden" animate="visible">
        <DeleteAccountSection />
      </motion.div>
    </div>
  );
}
