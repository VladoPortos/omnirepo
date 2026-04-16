/**
 * Admin Users page (D-22).
 * CRUD table with create/edit/delete modals, dicebear avatars,
 * one-time password reveal on create.
 */

import { useState, useCallback, useMemo } from 'react';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { api } from '@/api/client';
import type {
  User,
  UserCreate,
  UserCreateResponse,
  UserUpdate,
  PaginatedResponse,
} from '@/api/types';
import { DataTable, type ColumnDef } from '@/components/common/DataTable';
import { OneTimeReveal } from '@/components/common/OneTimeReveal';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
  DialogFooter,
} from '@/components/ui/dialog';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { Switch } from '@/components/ui/switch';
import { Avatar, AvatarImage, AvatarFallback } from '@/components/ui/avatar';
import { toast } from 'sonner';
import { formatDate } from '@/lib/format';
import { Plus, Pencil, Trash2, ShieldCheck } from 'lucide-react';
import { createAvatar } from '@dicebear/core';
import { initials } from '@dicebear/collection';

// ---------- API hooks ----------

function useAdminUsers(cursor?: string) {
  const params: Record<string, string> = {};
  if (cursor) params.cursor = cursor;
  return useQuery({
    queryKey: ['admin', 'users', cursor],
    queryFn: () => api.get<PaginatedResponse<User>>('/admin/users', params),
    staleTime: 15_000,
  });
}

function useAdminCreateUser() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (data: UserCreate) =>
      api.post<UserCreateResponse>('/admin/users', data),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['admin', 'users'] }),
  });
}

function useAdminUpdateUser() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, data }: { id: number; data: UserUpdate & { must_change_password?: boolean } }) =>
      api.patch<User>(`/admin/users/${id}`, data),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['admin', 'users'] }),
  });
}

function useAdminDeleteUser() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: number) => api.del<void>(`/admin/users/${id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['admin', 'users'] }),
  });
}

// ---------- Avatar helper ----------

function DicebearAvatar({ seed }: { seed: string }) {
  const dataUri = useMemo(() => {
    const svg = createAvatar(initials, { seed, size: 32 }).toString();
    return `data:image/svg+xml;utf8,${encodeURIComponent(svg)}`;
  }, [seed]);

  return (
    <Avatar size="sm">
      <AvatarImage src={dataUri} alt={seed} />
      <AvatarFallback>{seed.charAt(0).toUpperCase()}</AvatarFallback>
    </Avatar>
  );
}

// ---------- Component ----------

export default function UsersPage() {
  const [cursor, setCursor] = useState<string | undefined>();
  const { data, isLoading } = useAdminUsers(cursor);
  const createMutation = useAdminCreateUser();
  const updateMutation = useAdminUpdateUser();
  const deleteMutation = useAdminDeleteUser();

  // Dialog states
  const [createOpen, setCreateOpen] = useState(false);
  const [editUser, setEditUser] = useState<User | null>(null);
  const [deleteUser, setDeleteUser] = useState<User | null>(null);
  const [revealOpen, setRevealOpen] = useState(false);
  const [oneTimePassword, setOneTimePassword] = useState('');

  // Create form state
  const [createLogin, setCreateLogin] = useState('');
  const [createEmail, setCreateEmail] = useState('');

  // Edit form state
  const [editEmail, setEditEmail] = useState('');
  const [editSuperAdmin, setEditSuperAdmin] = useState(false);
  const [editForceReset, setEditForceReset] = useState(false);

  const handleCreate = useCallback(async () => {
    try {
      const result = await createMutation.mutateAsync({
        login: createLogin,
        email: createEmail,
      });
      setCreateOpen(false);
      setCreateLogin('');
      setCreateEmail('');
      setOneTimePassword(result.one_time_password);
      setRevealOpen(true);
      toast.success(`User "${result.login}" created.`);
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to create user');
    }
  }, [createLogin, createEmail, createMutation]);

  const handleEdit = useCallback(async () => {
    if (!editUser) return;
    try {
      await updateMutation.mutateAsync({
        id: editUser.id,
        data: {
          email: editEmail || undefined,
          is_super_admin: editSuperAdmin,
          must_change_password: editForceReset || undefined,
        },
      });
      setEditUser(null);
      toast.success('User updated.');
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to update user');
    }
  }, [editUser, editEmail, editSuperAdmin, editForceReset, updateMutation]);

  const handleDelete = useCallback(async () => {
    if (!deleteUser) return;
    try {
      await deleteMutation.mutateAsync(deleteUser.id);
      setDeleteUser(null);
      toast.success(`User "${deleteUser.login}" deleted.`);
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to delete user');
    }
  }, [deleteUser, deleteMutation]);

  const openEdit = useCallback((user: User) => {
    setEditEmail(user.email);
    setEditSuperAdmin(user.is_super_admin);
    setEditForceReset(false);
    setEditUser(user);
  }, []);

  const columns: ColumnDef<User>[] = [
    {
      id: 'avatar',
      name: '',
      className: 'w-10',
      render: (row) => <DicebearAvatar seed={row.avatar_seed || row.login} />,
    },
    {
      id: 'login',
      name: 'Login',
      sortable: true,
      render: (row) => (
        <span className="font-medium">{row.login}</span>
      ),
    },
    {
      id: 'email',
      name: 'Email',
      accessor: (row) => row.email,
    },
    {
      id: 'role',
      name: 'Role',
      render: (row) =>
        row.is_super_admin ? (
          <Badge variant="default">
            <ShieldCheck className="mr-1 size-3" />
            Super Admin
          </Badge>
        ) : (
          <Badge variant="secondary">User</Badge>
        ),
    },
    {
      id: 'created_at',
      name: 'Created',
      sortable: true,
      render: (row) => (
        <span className="text-muted-foreground text-xs">{formatDate(row.created_at)}</span>
      ),
    },
    {
      id: 'actions',
      name: '',
      className: 'w-24 text-right',
      render: (row) => (
        <div className="flex justify-end gap-1">
          <Button variant="ghost" size="icon-xs" onClick={() => openEdit(row)}>
            <Pencil className="size-3.5" />
          </Button>
          <Button variant="ghost" size="icon-xs" onClick={() => setDeleteUser(row)}>
            <Trash2 className="size-3.5 text-destructive" />
          </Button>
        </div>
      ),
    },
  ];

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-semibold">Users</h1>
          <p className="text-sm text-muted-foreground">
            Manage user accounts for this OmniRepo instance.
          </p>
        </div>
        <Button onClick={() => setCreateOpen(true)}>
          <Plus className="mr-1 size-4" data-icon="inline-start" />
          Create User
        </Button>
      </div>

      <DataTable
        columns={columns}
        data={data?.items ?? []}
        loading={isLoading}
        emptyMessage="No users found."
        pagination={
          data?.next_cursor
            ? {
                cursor: data.next_cursor,
                hasMore: !!data.next_cursor,
                onLoadMore: () => setCursor(data.next_cursor ?? undefined),
              }
            : undefined
        }
      />

      {/* Create User Dialog */}
      <Dialog open={createOpen} onOpenChange={setCreateOpen}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>Create User</DialogTitle>
            <DialogDescription>
              A one-time password will be generated. Share it securely with the user.
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-4">
            <div className="space-y-2">
              <Label htmlFor="create-login">Login</Label>
              <Input
                id="create-login"
                value={createLogin}
                onChange={(e) => setCreateLogin(e.target.value)}
                placeholder="e.g. jdoe"
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="create-email">Email</Label>
              <Input
                id="create-email"
                type="email"
                value={createEmail}
                onChange={(e) => setCreateEmail(e.target.value)}
                placeholder="e.g. jdoe@example.com"
              />
            </div>
          </div>
          <DialogFooter>
            <Button
              variant="outline"
              onClick={() => setCreateOpen(false)}
            >
              Cancel
            </Button>
            <Button
              onClick={handleCreate}
              disabled={!createLogin || !createEmail || createMutation.isPending}
            >
              {createMutation.isPending ? 'Creating...' : 'Create User'}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* One-Time Password Reveal */}
      <OneTimeReveal
        open={revealOpen}
        onOpenChange={setRevealOpen}
        title="One-Time Password"
        secret={oneTimePassword}
        warningText="This password will not be shown again. Copy it now and share it securely with the user."
      />

      {/* Edit User Dialog */}
      <Dialog open={!!editUser} onOpenChange={(open) => { if (!open) setEditUser(null); }}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>Edit User: {editUser?.login}</DialogTitle>
            <DialogDescription>Update user settings.</DialogDescription>
          </DialogHeader>
          <div className="space-y-4">
            <div className="space-y-2">
              <Label htmlFor="edit-email">Email</Label>
              <Input
                id="edit-email"
                type="email"
                value={editEmail}
                onChange={(e) => setEditEmail(e.target.value)}
              />
            </div>
            <div className="flex items-center justify-between">
              <Label htmlFor="edit-admin">Super Admin</Label>
              <Switch
                id="edit-admin"
                checked={editSuperAdmin}
                onCheckedChange={setEditSuperAdmin}
              />
            </div>
            <div className="flex items-center justify-between">
              <Label htmlFor="edit-reset">Force Password Reset</Label>
              <Switch
                id="edit-reset"
                checked={editForceReset}
                onCheckedChange={setEditForceReset}
              />
            </div>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setEditUser(null)}>
              Cancel
            </Button>
            <Button
              onClick={handleEdit}
              disabled={updateMutation.isPending}
            >
              {updateMutation.isPending ? 'Saving...' : 'Save Changes'}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Delete User Confirmation */}
      <Dialog open={!!deleteUser} onOpenChange={(open) => { if (!open) setDeleteUser(null); }}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete User</DialogTitle>
            <DialogDescription>
              Are you sure you want to delete user <strong>{deleteUser?.login}</strong>? This action cannot be undone.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setDeleteUser(null)}>
              Cancel
            </Button>
            <Button
              variant="destructive"
              onClick={handleDelete}
              disabled={deleteMutation.isPending}
            >
              {deleteMutation.isPending ? 'Deleting...' : 'Delete User'}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
