/**
 * Project detail page.
 * Breadcrumb, tabs per repo type, overview with members + activity.
 */

import { useState, useMemo, useCallback, type FormEvent } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { useParams, useNavigate, Link } from 'react-router-dom';
import { motion } from 'framer-motion';
import { Plus, Users, Activity, FolderGit2, Trash2 } from 'lucide-react';
import { EmptyState } from '@/components/common/EmptyState';
import { toast } from 'sonner';
import { Tabs, TabsList, TabsTrigger, TabsContent } from '@/components/ui/tabs';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { Skeleton } from '@/components/ui/skeleton';
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogFooter,
} from '@/components/ui/dialog';
import { Badge } from '@/components/ui/badge';
import { Avatar, AvatarFallback } from '@/components/ui/avatar';
import { TypeBadge } from '@/components/common/TypeBadge';
import { Tooltip, TooltipTrigger, TooltipContent } from '@/components/ui/tooltip';
import { CreateRepoDialog } from '@/components/CreateRepoDialog';
import { ProjectAPIKeysCard } from '@/components/ProjectAPIKeysCard';
import {
  useProject,
  useProjectActivity,
  useDeleteProject,
  useProjectBuckets,
  useCreateBucket,
  useDeleteBucket,
  useAddProjectMember,
  useRemoveProjectMember,
  useUpdateProjectMemberRole,
  useAdminUserList,
  useMe,
} from '@/api/queries';
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select';
import { useRoleFor } from '@/hooks/useAuth';
import { formatBytes, formatDate } from '@/lib/format';
import { bucketNameSeemsValid } from '@/lib/validators';
import {
  envelopeFromError,
  fieldErrorsFromEnvelope,
  type ApiErrorEnvelope,
} from '@/api/client';
import { ErrorEnvelopeRenderer } from '@/components/common/ErrorEnvelope';
import type { RepoType, ProjectRepo, ProjectBucket, ProjectMember } from '@/api/types';

const REPO_TYPES: { value: RepoType; label: string }[] = [
  { value: 'docker', label: 'Docker' },
  { value: 'rpm', label: 'RPM' },
  { value: 'deb', label: 'APT' },
  { value: 'pypi', label: 'PyPI' },
  { value: 'helm', label: 'Helm' },
  { value: 'go', label: 'Go' },
  { value: 'git', label: 'Git' },
  { value: 'raw', label: 'RAW' },
  { value: 's3', label: 'S3' },
];

// The creatable-type allowlist now lives inside CreateRepoDialog
// (which also owns mirror-protocol gating).
// S3 is managed via /s3-buckets/, not /repos.

const ALL_TABS = ['overview', ...REPO_TYPES.map((t) => t.value)] as const;

export function ProjectDetailPage() {
  const { name = '' } = useParams<{ name: string }>();
  const navigate = useNavigate();
  const { data: project, isLoading } = useProject(name);
  const { data: activityData } = useProjectActivity(name);
  const deleteProject = useDeleteProject();

  const [activeTab, setActiveTab] = useState<string>('overview');
  const [dialogOpen, setDialogOpen] = useState(false);
  const [dialogInitialType, setDialogInitialType] = useState<RepoType>('docker');
  const [deleteOpen, setDeleteOpen] = useState(false);
  const [deleteConfirm, setDeleteConfirm] = useState('');
  const [deleteError, setDeleteError] = useState<ApiErrorEnvelope | null>(null);

  // Member picker state
  const [memberOpen, setMemberOpen] = useState(false);
  const [memberLogin, setMemberLogin] = useState('');
  const [memberError, setMemberError] = useState<ApiErrorEnvelope | null>(null);
  const [removeTarget, setRemoveTarget] = useState<{ login: string } | null>(null);
  const [addMemberRole, setAddMemberRole] = useState<'maintainer' | 'viewer'>('viewer');
  const [demoteTarget, setDemoteTarget] = useState<{ login: string; isSelf: boolean } | null>(null);
  const { data: me } = useMe();
  // /admin/users is super-admin-only — gating the picker fetch prevents
  // non-admin project members from flooding the console with 403s on
  // every page load + every Add Member click. The picker is only useful
  // for members who can actually add users anyway.
  const canListUsers = !!me?.is_super_admin;
  const { data: userListData } = useAdminUserList({ enabled: canListUsers });
  const addMember = useAddProjectMember(name);
  const removeMember = useRemoveProjectMember(name);
  const updateRole = useUpdateProjectMemberRole(name);

  // Role of the current user in this project (super-admins map to 'maintainer').
  const myRole = useRoleFor(name);
  const isMaintainer = myRole === 'maintainer';
  const qc = useQueryClient();
  // openAddMember invalidates the cached users list so the picker sees
  // accounts added since this page mounted (the underlying hook is
  // mounted at page-load and cached with a 30s staleTime, so without this
  // a user created in another tab or via API wouldn't appear until a full
  // page reload). Skip the invalidate for non-admin viewers — otherwise
  // we'd trigger the 403 we just gated against.
  const openAddMember = useCallback(() => {
    setMemberError(null);
    setMemberLogin('');
    setMemberOpen(true);
    if (canListUsers) {
      qc.invalidateQueries({ queryKey: ['admin', 'users', 'list'] });
    }
  }, [qc, canListUsers]);

  // Last-maintainer guard: count only members with role==='maintainer'.
  const maintainerCount = useMemo(
    () => (project?.members ?? []).filter((m) => m.role === 'maintainer').length,
    [project],
  );
  const isLastMaintainerRow = (m: ProjectMember) =>
    m.role === 'maintainer' && maintainerCount === 1;

  /** Map a role-update error to a user-facing message. */
  function roleErrorMessage(err: unknown): string {
    const env = envelopeFromError(err, 'Failed to update role.');
    if (env.code === 'codeRBACLastMaintainer') return env.message;
    if (env.code === 'auth.not_a_maintainer') return 'Maintainer role required for this action.';
    return env.message;
  }

  // Group repos by type
  const reposByType = useMemo(() => {
    const map: Record<string, ProjectRepo[]> = {};
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
    const repoBytes =
      project?.repos?.reduce((sum, r) => sum + r.size_bytes, 0) ?? 0;
    const bucketBytes =
      project?.buckets?.reduce((sum, b) => sum + b.size_bytes, 0) ?? 0;
    return repoBytes + bucketBytes;
  }, [project]);

  const bucketCount = project?.buckets?.length ?? 0;

  const openCreateDialog = (preselectedType?: RepoType) => {
    if (preselectedType) setDialogInitialType(preselectedType);
    setDialogOpen(true);
  };

  if (isLoading) {
    return (
      <div className="space-y-6">
        <Skeleton className="h-5 w-48" />
        <Skeleton className="h-8 w-64" />
        <Skeleton className="h-64 w-full" />
      </div>
    );
  }

  if (!project) {
    return (
      <div className="text-center py-12">
        <h2 className="text-lg font-semibold">Project not found</h2>
        <p className="mt-2 text-sm text-muted-foreground">
          The project &quot;{name}&quot; does not exist or you lack access.
        </p>
        <Button className="mt-4" nativeButton={false} render={<Link to="/projects" />}>
          Back to Projects
        </Button>
      </div>
    );
  }

  const activity = activityData?.items ?? [];

  return (
    <div className="space-y-6">
      {/* Title */}
      <div className="flex items-start justify-between gap-4">
        <div>
          <h1 className="text-[28px] font-semibold leading-tight">
            {project.name}
          </h1>
          {project.description_md && (
            <p className="mt-1 text-sm text-muted-foreground">
              {project.description_md}
            </p>
          )}
        </div>
        <Button
          variant="ghost"
          size="sm"
          className="text-destructive hover:bg-destructive/10"
          onClick={() => {
            setDeleteError(null);
            setDeleteConfirm('');
            setDeleteOpen(true);
          }}
        >
          <Trash2 className="mr-1.5 size-4" />
          Delete Project
        </Button>
      </div>

      <Dialog open={deleteOpen} onOpenChange={setDeleteOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete project &quot;{project.name}&quot;?</DialogTitle>
          </DialogHeader>
          <div className="space-y-4 py-4">
            <p className="text-sm text-muted-foreground">
              This soft-deletes the project. Repositories, artifacts, and members
              are retained for the configured retention window and can be restored
              by an administrator until then.
            </p>
            {deleteError && (
              <ErrorEnvelopeRenderer envelope={deleteError} />
            )}
            <div className="space-y-2">
              <Label htmlFor="delete-confirm">
                Type <span className="font-mono font-semibold">{project.name}</span> to confirm
              </Label>
              <Input
                id="delete-confirm"
                value={deleteConfirm}
                onChange={(e) => setDeleteConfirm(e.target.value)}
                autoFocus
              />
            </div>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setDeleteOpen(false)}>
              Cancel
            </Button>
            <Button
              variant="destructive"
              disabled={deleteConfirm !== project.name || deleteProject.isPending}
              onClick={async () => {
                try {
                  await deleteProject.mutateAsync(project.name);
                  toast.success(`Project "${project.name}" deleted.`);
                  setDeleteOpen(false);
                  navigate('/projects');
                } catch (err) {
                  setDeleteError(envelopeFromError(err, 'Failed to delete project.'));
                }
              }}
            >
              {deleteProject.isPending ? 'Deleting...' : 'Delete Project'}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Tabs */}
      <Tabs
        value={activeTab}
        onValueChange={(val) => setActiveTab(val as string)}
      >
        <TabsList variant="line" className="w-full justify-start overflow-x-auto overflow-y-hidden pb-1.5">
          {ALL_TABS.map((tab) => (
            <TabsTrigger key={tab} value={tab} className="flex-none px-3">
              {tab === 'overview'
                ? 'Overview'
                : REPO_TYPES.find((t) => t.value === tab)?.label ?? tab}
              {tab === 's3'
                ? bucketCount > 0 && (
                    <span className="ml-1 text-xs text-muted-foreground tabular-nums">
                      ({bucketCount})
                    </span>
                  )
                : tab !== 'overview' && reposByType[tab]?.length > 0 && (
                    <span className="ml-1 text-xs text-muted-foreground tabular-nums">
                      ({reposByType[tab].length})
                    </span>
                  )}
            </TabsTrigger>
          ))}
        </TabsList>

        {/* Overview Tab */}
        <TabsContent value="overview">
          <div className="mt-4 grid gap-6 lg:grid-cols-2">
            {/* Members */}
            <Card>
              <CardHeader>
                <div className="flex items-center justify-between">
                  <div className="flex items-center gap-2">
                    <Users className="size-4 text-muted-foreground" />
                    <CardTitle>Members</CardTitle>
                  </div>
                  {isMaintainer && (
                    <Button
                      variant="outline"
                      size="sm"
                      onClick={openAddMember}
                    >
                      Add Member
                    </Button>
                  )}
                </div>
              </CardHeader>
              <CardContent>
                {project.members.filter((m) => m.login !== me?.login).length === 0 ? (
                  <EmptyState
                    icon={Users}
                    title="No teammates yet"
                    description="Add a teammate so someone else can publish to this project."
                    primaryCTA={
                      isMaintainer
                        ? { label: 'Add member', onClick: openAddMember }
                        : { label: 'Add member', disabled: true, disabledHint: 'Maintainer role required for this action.' }
                    }
                  />
                ) : (
                  <div className="space-y-2">
                    {project.members.map((m) => (
                      <div
                        key={m.user_id}
                        className="flex items-center justify-between gap-3 text-sm"
                      >
                        <div className="flex items-center gap-3">
                          <Avatar size="sm">
                            <AvatarFallback>
                              {m.login.slice(0, 2).toUpperCase()}
                            </AvatarFallback>
                          </Avatar>
                          <div>
                            <p className="font-medium">{m.login}</p>
                            <p className="text-xs text-muted-foreground">
                              {m.email}
                            </p>
                          </div>
                        </div>
                        {isMaintainer ? (
                          <div className="flex items-center gap-2">
                            {/* Role badge */}
                            {m.role === 'maintainer' ? (
                              <Badge variant="default">Maintainer</Badge>
                            ) : (
                              <Badge variant="secondary">Viewer</Badge>
                            )}
                            {/* Role select */}
                            <Select
                              value={m.role}
                              onValueChange={(val) => {
                                if (val === m.role) return;
                                if (val === 'viewer') {
                                  setDemoteTarget({ login: m.login, isSelf: m.login === me?.login });
                                } else {
                                  updateRole.mutate(
                                    { login: m.login, role: 'maintainer' },
                                    {
                                      onSuccess: () => toast.success('Role updated.'),
                                      onError: (err) => toast.error(roleErrorMessage(err)),
                                    },
                                  );
                                }
                              }}
                            >
                              <SelectTrigger size="sm">
                                <SelectValue />
                              </SelectTrigger>
                              <SelectContent>
                                <SelectItem value="maintainer">Maintainer</SelectItem>
                                <SelectItem value="viewer" disabled={isLastMaintainerRow(m)}>Viewer</SelectItem>
                              </SelectContent>
                            </Select>
                            {/* Trash button — disabled+tooltip for last maintainer */}
                            {isLastMaintainerRow(m) ? (
                              <Tooltip>
                                <TooltipTrigger
                                  render={
                                    <span
                                      className="inline-block rounded-md focus-visible:outline-hidden focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2"
                                      tabIndex={0}
                                      role="button"
                                      aria-disabled="true"
                                      aria-label={`Cannot remove ${m.login} — last maintainer`}
                                    >
                                      <Button
                                        variant="ghost"
                                        size="sm"
                                        className="text-destructive hover:bg-destructive/10"
                                        disabled
                                        tabIndex={-1}
                                      >
                                        <Trash2 className="size-4" />
                                      </Button>
                                    </span>
                                  }
                                />
                                <TooltipContent>Promote another member to maintainer first.</TooltipContent>
                              </Tooltip>
                            ) : (
                              <Button
                                variant="ghost"
                                size="sm"
                                className="text-destructive hover:bg-destructive/10"
                                onClick={() => setRemoveTarget({ login: m.login })}
                                aria-label={`Remove ${m.login}`}
                              >
                                <Trash2 className="size-4" />
                              </Button>
                            )}
                          </div>
                        ) : (
                          <div className="flex items-center">
                            {m.role === 'maintainer' ? (
                              <Badge variant="default">Maintainer</Badge>
                            ) : (
                              <Badge variant="secondary">Viewer</Badge>
                            )}
                          </div>
                        )}
                      </div>
                    ))}
                  </div>
                )}
              </CardContent>
            </Card>

            {/* Add Member dialog */}
            <Dialog open={memberOpen} onOpenChange={(open) => { setMemberOpen(open); if (!open) { setMemberError(null); setAddMemberRole('viewer'); } }}>
              <DialogContent>
                <DialogHeader>
                  <DialogTitle>Add member to {project.name}</DialogTitle>
                </DialogHeader>
                <div className="space-y-4 py-2">
                  {memberError && <ErrorEnvelopeRenderer envelope={memberError} />}
                  <div className="space-y-2">
                    <Label>User</Label>
                    <Select
                      value={memberLogin}
                      onValueChange={(val) => setMemberLogin(val ?? '')}
                    >
                      <SelectTrigger className="w-full">
                        <SelectValue placeholder="Select a user" />
                      </SelectTrigger>
                      <SelectContent>
                        {(userListData?.items ?? [])
                          .filter(
                            (u) =>
                              !project.members.some((m) => m.login === u.login),
                          )
                          .map((u) => (
                            <SelectItem key={u.id} value={u.login}>
                              {u.login}
                              {u.email ? ` — ${u.email}` : ''}
                            </SelectItem>
                          ))}
                      </SelectContent>
                    </Select>
                    <p className="text-xs text-muted-foreground">
                      Super-admins always have implicit access; this picker lists
                      only users who can be explicitly assigned.
                    </p>
                  </div>
                  <div className="space-y-2">
                    <Label htmlFor="add-member-role">Role</Label>
                    <Select
                      value={addMemberRole}
                      onValueChange={(v) => setAddMemberRole(v as 'maintainer' | 'viewer')}
                    >
                      <SelectTrigger id="add-member-role" className="w-full">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value="viewer">Viewer</SelectItem>
                        <SelectItem value="maintainer">Maintainer</SelectItem>
                      </SelectContent>
                    </Select>
                  </div>
                </div>
                <DialogFooter>
                  <Button variant="outline" onClick={() => setMemberOpen(false)}>
                    Cancel
                  </Button>
                  <Button
                    disabled={!memberLogin || addMember.isPending}
                    onClick={async () => {
                      try {
                        await addMember.mutateAsync({ login: memberLogin, role: addMemberRole });
                        toast.success(`${memberLogin} added to ${project.name}.`);
                        setMemberOpen(false);
                        setAddMemberRole('viewer');
                      } catch (err) {
                        setMemberError(
                          envelopeFromError(err, 'Failed to add member.'),
                        );
                      }
                    }}
                  >
                    {addMember.isPending ? 'Adding…' : 'Add Member'}
                  </Button>
                </DialogFooter>
              </DialogContent>
            </Dialog>

            {/* Demote confirmation dialog */}
            <Dialog open={!!demoteTarget} onOpenChange={(open) => !open && setDemoteTarget(null)}>
              <DialogContent>
                <DialogHeader>
                  <DialogTitle>
                    {demoteTarget?.isSelf
                      ? 'Give up maintainer access?'
                      : `Change ${demoteTarget?.login} to Viewer?`}
                  </DialogTitle>
                </DialogHeader>
                <p className="py-2 text-sm text-muted-foreground">
                  {demoteTarget?.isSelf
                    ? 'You will lose write access to this project. Another maintainer or a super-admin will need to promote you back.'
                    : 'They will lose write access to this project. Maintainers can promote them back anytime.'}
                </p>
                <DialogFooter>
                  <Button variant="outline" onClick={() => setDemoteTarget(null)}>
                    Cancel
                  </Button>
                  <Button
                    variant="default"
                    disabled={updateRole.isPending}
                    onClick={() => {
                      if (!demoteTarget) return;
                      updateRole.mutate(
                        { login: demoteTarget.login, role: 'viewer' },
                        {
                          onSuccess: () => {
                            toast.success('Role updated.');
                            setDemoteTarget(null);
                          },
                          onError: (err) => toast.error(roleErrorMessage(err)),
                        },
                      );
                    }}
                  >
                    {updateRole.isPending ? 'Saving…' : 'Confirm'}
                  </Button>
                </DialogFooter>
              </DialogContent>
            </Dialog>

            {/* Remove confirmation */}
            <Dialog
              open={!!removeTarget}
              onOpenChange={(open) => !open && setRemoveTarget(null)}
            >
              <DialogContent>
                <DialogHeader>
                  <DialogTitle>
                    Remove {removeTarget?.login} from {project.name}?
                  </DialogTitle>
                </DialogHeader>
                <p className="py-2 text-sm text-muted-foreground">
                  They will lose access to this project immediately. You can add
                  them back at any time.
                </p>
                <DialogFooter>
                  <Button
                    variant="outline"
                    onClick={() => setRemoveTarget(null)}
                  >
                    Cancel
                  </Button>
                  <Button
                    variant="destructive"
                    disabled={removeMember.isPending}
                    onClick={async () => {
                      if (!removeTarget) return;
                      try {
                        await removeMember.mutateAsync(removeTarget.login);
                        toast.success(`${removeTarget.login} removed.`);
                        setRemoveTarget(null);
                      } catch (err) {
                        toast.error(
                          envelopeFromError(err, 'Failed to remove member.').message,
                        );
                      }
                    }}
                  >
                    {removeMember.isPending ? 'Removing…' : 'Remove'}
                  </Button>
                </DialogFooter>
              </DialogContent>
            </Dialog>

            {/* Storage — plain total, no gauge. OmniRepo v1 has no
                per-project quota, so the old `used of max(2×used, 1 GB)`
                ratio was a placeholder denominator that confused operators
                (e.g. "Where does the 1 GB limit come from?"). The admin
                Dashboard carries the global used/disk gauge; this card
                stays informational. */}
            <Card>
              <CardHeader>
                <CardTitle>Storage</CardTitle>
              </CardHeader>
              <CardContent>
                <p className="text-3xl font-semibold tabular-nums">
                  {formatBytes(totalSize)}
                </p>
                <p className="mt-2 text-xs text-muted-foreground">
                  {project.repos?.length ?? 0} repositories
                  {bucketCount > 0
                    ? `, ${bucketCount} S3 bucket${bucketCount === 1 ? '' : 's'}`
                    : ''}
                </p>
              </CardContent>
            </Card>

            {/* Project-scoped API keys (omr_p_*) for CI pipelines */}
            <ProjectAPIKeysCard projectName={name} />

            {/* Activity feed */}
            <Card className="lg:col-span-2">
              <CardHeader>
                <div className="flex items-center gap-2">
                  <Activity className="size-4 text-muted-foreground" />
                  <CardTitle>Project Activity</CardTitle>
                </div>
              </CardHeader>
              <CardContent>
                {activity.length === 0 ? (
                  <p className="text-sm text-muted-foreground">
                    No activity yet.
                  </p>
                ) : (
                  <div className="max-h-[400px] space-y-3 overflow-y-auto">
                    {activity.slice(0, 50).map((event) => (
                      <div
                        key={event.id}
                        className="flex items-start gap-3 text-sm"
                      >
                        <span className="shrink-0 text-xs text-muted-foreground tabular-nums">
                          {formatDate(event.created_at)}
                        </span>
                        <span className="flex-1">
                          {event.action}{' '}
                          <span className="text-muted-foreground">
                            {event.target_kind}/{event.target_id}
                          </span>
                        </span>
                      </div>
                    ))}
                  </div>
                )}
              </CardContent>
            </Card>
          </div>
        </TabsContent>

        {/* Type tabs */}
        {REPO_TYPES.map((rt) => {
          // S3 tab has its own data source (buckets, not repos) and its
          // own Create dialog — render a dedicated component.
          if (rt.value === 's3') {
            return (
              <TabsContent key="s3" value="s3">
                <S3BucketsTab projectName={name} />
              </TabsContent>
            );
          }
          return (
          <TabsContent key={rt.value} value={rt.value}>
            <div className="mt-4 space-y-4">
              <div className="flex items-center justify-between">
                <h2 className="text-lg font-semibold">{rt.label} Repositories</h2>
                <Button
                  size="sm"
                  onClick={() => openCreateDialog(rt.value)}
                >
                  <Plus className="mr-1.5 size-4" />
                  Create Repository
                </Button>
              </div>

              {reposByType[rt.value].length === 0 ? (
                <EmptyState
                  icon={FolderGit2}
                  title="No repositories yet"
                  description="Add a repository to start publishing artifacts to this project."
                  primaryCTA={{
                    label: 'Create repository',
                    onClick: () => openCreateDialog(rt.value),
                  }}
                />
              ) : (
                <div className="space-y-2">
                  {reposByType[rt.value].map((repo) => (
                    <motion.div
                      key={repo.id}
                      initial={{ opacity: 0, y: 4 }}
                      animate={{ opacity: 1, y: 0 }}
                      transition={{ duration: 0.15 }}
                    >
                      <Link
                        to={`/projects/${name}/${repo.type}/${repo.name}`}
                        className="block focus:outline-none focus-visible:ring-2 focus-visible:ring-ring rounded-xl"
                      >
                        <Card className="transition-all duration-150 hover:-translate-y-0.5 hover:shadow-md">
                          <CardContent className="flex items-center justify-between py-3">
                            <div className="flex items-center gap-3">
                              <TypeBadge type={repo.type as RepoType} />
                              <div>
                                <p className="font-medium">{repo.name}</p>
                                {repo.description_md && (
                                  <p className="text-xs text-muted-foreground line-clamp-1">
                                    {repo.description_md}
                                  </p>
                                )}
                              </div>
                            </div>
                            <div className="flex items-center gap-4 text-xs text-muted-foreground">
                              <span>{formatBytes(repo.size_bytes)}</span>
                              <span>
                                Created {formatDate(repo.created_at)}
                              </span>
                            </div>
                          </CardContent>
                        </Card>
                      </Link>
                    </motion.div>
                  ))}
                </div>
              )}
            </div>
          </TabsContent>
          );
        })}
      </Tabs>

      {/* Create Repo Dialog (extracted to CreateRepoDialog for mirror
          config wiring) */}
      <CreateRepoDialog
        open={dialogOpen}
        onClose={() => setDialogOpen(false)}
        projectName={name}
        initialType={dialogInitialType}
        onCreated={(repo) => setActiveTab(repo.type)}
      />
    </div>
  );
}

// S3BucketsTab renders the S3 tab: bucket list from the dedicated buckets
// endpoint (repos table doesn't include buckets), a "Create Bucket" dialog,
// and per-bucket navigation to the detail/object-browser page.
function S3BucketsTab({ projectName }: { projectName: string }) {
  const { data: buckets, isLoading, isError } = useProjectBuckets(projectName);
  const createBucket = useCreateBucket(projectName);
  const deleteBucket = useDeleteBucket(projectName);

  const [dialogOpen, setDialogOpen] = useState(false);
  const [bucketName, setBucketName] = useState('');
  const [createError, setCreateError] = useState<ApiErrorEnvelope | null>(null);
  const createFieldErrors = fieldErrorsFromEnvelope(createError);

  const [deleteTarget, setDeleteTarget] = useState<ProjectBucket | null>(null);
  const [deleteError, setDeleteError] = useState<ApiErrorEnvelope | null>(null);

  const onCreate = async (e: FormEvent) => {
    e.preventDefault();
    setCreateError(null);
    try {
      await createBucket.mutateAsync({ name: bucketName.trim() });
      toast.success(`Bucket "${bucketName}" created.`);
      setBucketName('');
      setDialogOpen(false);
    } catch (err) {
      setCreateError(envelopeFromError(err, 'Failed to create bucket.'));
    }
  };

  const onDelete = async () => {
    if (!deleteTarget) return;
    setDeleteError(null);
    try {
      await deleteBucket.mutateAsync(deleteTarget.name);
      toast.success(`Bucket "${deleteTarget.name}" deleted.`);
      setDeleteTarget(null);
    } catch (err) {
      setDeleteError(envelopeFromError(err, 'Failed to delete bucket.'));
    }
  };

  return (
    <div className="mt-4 space-y-4">
      <div className="flex items-center justify-between">
        <h2 className="text-lg font-semibold">S3 Buckets</h2>
        <Button size="sm" onClick={() => setDialogOpen(true)}>
          <Plus className="mr-1.5 size-4" />
          Create Bucket
        </Button>
      </div>

      {isLoading ? (
        <Skeleton className="h-24 w-full" />
      ) : isError ? (
        <div className="rounded-md bg-destructive/10 p-3 text-sm text-destructive">
          Failed to load buckets.
        </div>
      ) : (buckets?.length ?? 0) === 0 ? (
        <div className="flex flex-col items-center justify-center rounded-lg border border-dashed p-12 text-center">
          <FolderGit2 className="size-12 text-muted-foreground/50" />
          <h3 className="mt-4 text-lg font-semibold">No buckets</h3>
          <p className="mt-2 max-w-md text-sm text-muted-foreground">
            Create your first S3 bucket. Buckets are addressable at
            <code className="ml-1 rounded bg-muted px-1 py-0.5 font-mono text-xs">
              /s3/{'{'}bucket{'}'}/{'{'}key{'}'}
            </code>{' '}
            and require SigV4-signed requests.
          </p>
          <Button className="mt-6" onClick={() => setDialogOpen(true)}>
            <Plus className="mr-1.5 size-4" />
            Create Bucket
          </Button>
        </div>
      ) : (
        <div className="space-y-2">
          {(buckets ?? []).map((b) => (
            <motion.div
              key={b.id}
              initial={{ opacity: 0, y: 4 }}
              animate={{ opacity: 1, y: 0 }}
              transition={{ duration: 0.15 }}
            >
              <Card className="transition-all duration-150 hover:-translate-y-0.5 hover:shadow-md">
                <CardContent className="flex items-center justify-between py-3">
                  <Link
                    to={`/projects/${projectName}/s3/${b.name}`}
                    className="flex flex-1 items-center gap-3 focus:outline-none focus-visible:ring-2 focus-visible:ring-ring rounded-md"
                  >
                    <TypeBadge type="s3" />
                    <div>
                      <p className="font-medium">{b.name}</p>
                      <p className="text-xs text-muted-foreground">
                        {b.object_count} object{b.object_count === 1 ? '' : 's'} ·{' '}
                        {formatBytes(b.size_bytes)}
                      </p>
                    </div>
                  </Link>
                  <div className="flex items-center gap-4 text-xs text-muted-foreground">
                    <span>Created {formatDate(b.created_at)}</span>
                    <Button
                      variant="ghost"
                      size="icon-sm"
                      title="Delete bucket"
                      onClick={() => {
                        setDeleteError(null);
                        setDeleteTarget(b);
                      }}
                    >
                      <Trash2 className="size-4" />
                    </Button>
                  </div>
                </CardContent>
              </Card>
            </motion.div>
          ))}
        </div>
      )}

      {/* Create bucket dialog */}
      <Dialog open={dialogOpen} onOpenChange={setDialogOpen}>
        <DialogContent>
          <form onSubmit={onCreate}>
            <DialogHeader>
              <DialogTitle>Create S3 Bucket</DialogTitle>
            </DialogHeader>
            <div className="space-y-4 py-4">
              {createError && (
                <ErrorEnvelopeRenderer envelope={createError} />
              )}
              <div className="space-y-2">
                <Label htmlFor="bucket-name">Bucket name</Label>
                <Input
                  id="bucket-name"
                  value={bucketName}
                  onChange={(e) => setBucketName(e.target.value)}
                  placeholder="my-bucket"
                  required
                  autoFocus
                  aria-invalid={
                    !!createFieldErrors['name'] ||
                    !!createFieldErrors['bucket-name'] ||
                    undefined
                  }
                />
                <p className="text-xs text-muted-foreground">
                  Lowercase letters, digits, hyphens, and dots. 3–63 chars.
                </p>
              </div>
            </div>
            <DialogFooter>
              <Button variant="outline" type="button" onClick={() => setDialogOpen(false)}>
                Cancel
              </Button>
              <Button type="submit" disabled={createBucket.isPending || !bucketNameSeemsValid(bucketName)}>
                {createBucket.isPending ? 'Creating...' : 'Create Bucket'}
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>

      {/* Delete bucket confirm */}
      <Dialog open={!!deleteTarget} onOpenChange={(v) => !v && setDeleteTarget(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete bucket &quot;{deleteTarget?.name}&quot;?</DialogTitle>
          </DialogHeader>
          <div className="space-y-4 py-4">
            <p className="text-sm text-muted-foreground">
              The bucket must be empty. Objects are hard-deleted (no trash),
              so delete them first if you need them preserved.
            </p>
            {deleteTarget && deleteTarget.object_count > 0 && (
              <div className="rounded-md bg-destructive/10 p-3 text-sm text-destructive">
                Bucket still holds {deleteTarget.object_count} object
                {deleteTarget.object_count === 1 ? '' : 's'} (
                {formatBytes(deleteTarget.size_bytes)}). Delete will fail with 409.
              </div>
            )}
            {deleteError && (
              <ErrorEnvelopeRenderer envelope={deleteError} />
            )}
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setDeleteTarget(null)}>
              Cancel
            </Button>
            <Button
              variant="destructive"
              disabled={deleteBucket.isPending}
              onClick={onDelete}
            >
              {deleteBucket.isPending ? 'Deleting...' : 'Delete Bucket'}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
