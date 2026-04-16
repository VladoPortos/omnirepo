/**
 * Project detail page per D-04, D-27.
 * Breadcrumb, tabs per repo type, overview with members + activity.
 */

import { useState, useMemo, type FormEvent } from 'react';
import { useParams, useNavigate, Link } from 'react-router-dom';
import { motion } from 'framer-motion';
import { Plus, Users, Activity, FolderGit2 } from 'lucide-react';
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
import {
  Select,
  SelectTrigger,
  SelectValue,
  SelectContent,
  SelectItem,
} from '@/components/ui/select';
import { Avatar, AvatarFallback } from '@/components/ui/avatar';
import { TypeBadge } from '@/components/common/TypeBadge';
import { StorageGauge } from '@/components/common/StorageGauge';
import {
  useProject,
  useProjectActivity,
  useCreateRepo,
} from '@/api/queries';
import { formatBytes, formatDate } from '@/lib/format';
import { ApiError } from '@/api/client';
import type { RepoType, ProjectRepo } from '@/api/types';

const REPO_TYPES: { value: RepoType; label: string }[] = [
  { value: 'docker', label: 'Docker' },
  { value: 'rpm', label: 'RPM' },
  { value: 'deb', label: 'APT' },
  { value: 'pypi', label: 'PyPI' },
  { value: 'helm', label: 'Helm' },
  { value: 'git', label: 'Git' },
  { value: 'raw', label: 'RAW' },
  { value: 's3', label: 'S3' },
];

const ALL_TABS = ['overview', ...REPO_TYPES.map((t) => t.value)] as const;

export function ProjectDetailPage() {
  const { name = '' } = useParams<{ name: string }>();
  const navigate = useNavigate();
  const { data: project, isLoading } = useProject(name);
  const { data: activityData } = useProjectActivity(name);
  const createRepo = useCreateRepo();

  const [activeTab, setActiveTab] = useState<string>('overview');
  const [dialogOpen, setDialogOpen] = useState(false);
  const [repoName, setRepoName] = useState('');
  const [repoType, setRepoType] = useState<RepoType>(
    activeTab !== 'overview' ? (activeTab as RepoType) : 'docker',
  );
  const [createError, setCreateError] = useState('');

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
    return project?.repos?.reduce((sum, r) => sum + r.size_bytes, 0) ?? 0;
  }, [project]);

  const handleCreateRepo = async (e: FormEvent) => {
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
    } catch (err) {
      if (err instanceof ApiError) {
        setCreateError(err.detail);
      } else {
        setCreateError('Failed to create repository.');
      }
    }
  };

  const openCreateDialog = (preselectedType?: RepoType) => {
    if (preselectedType) setRepoType(preselectedType);
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

      {/* Tabs */}
      <Tabs
        value={activeTab}
        onValueChange={(val) => setActiveTab(val as string)}
      >
        <TabsList variant="line" className="w-full overflow-x-auto overflow-y-hidden pb-1.5">
          {ALL_TABS.map((tab) => (
            <TabsTrigger key={tab} value={tab}>
              {tab === 'overview'
                ? 'Overview'
                : REPO_TYPES.find((t) => t.value === tab)?.label ?? tab}
              {tab !== 'overview' && reposByType[tab]?.length > 0 && (
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
                  <Button variant="outline" size="sm" nativeButton={false} render={<Link to="/admin/users" />}>
                    Add Member
                  </Button>
                </div>
              </CardHeader>
              <CardContent>
                {project.members.length === 0 ? (
                  <p className="text-sm text-muted-foreground">No members.</p>
                ) : (
                  <div className="space-y-2">
                    {project.members.map((m) => (
                      <div
                        key={m.user_id}
                        className="flex items-center gap-3 text-sm"
                      >
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
                    ))}
                  </div>
                )}
              </CardContent>
            </Card>

            {/* Storage */}
            <Card>
              <CardHeader>
                <CardTitle>Storage</CardTitle>
              </CardHeader>
              <CardContent>
                <StorageGauge
                  used={totalSize}
                  total={Math.max(totalSize * 2, 1073741824)}
                />
                <p className="mt-3 text-xs text-muted-foreground">
                  {project.repos?.length ?? 0} repositories,{' '}
                  {formatBytes(totalSize)} total
                </p>
              </CardContent>
            </Card>

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
        {REPO_TYPES.map((rt) => (
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
                <div className="flex flex-col items-center justify-center rounded-lg border border-dashed p-12 text-center">
                  <FolderGit2 className="size-12 text-muted-foreground/50" />
                  <h3 className="mt-4 text-lg font-semibold">
                    No repositories
                  </h3>
                  <p className="mt-2 max-w-md text-sm text-muted-foreground">
                    Create your first {rt.label.toLowerCase()} repository
                  </p>
                  <Button
                    className="mt-6"
                    onClick={() => openCreateDialog(rt.value)}
                  >
                    <Plus className="mr-1.5 size-4" />
                    Create Repository
                  </Button>
                </div>
              ) : (
                <div className="space-y-2">
                  {reposByType[rt.value].map((repo) => (
                    <motion.div
                      key={repo.id}
                      initial={{ opacity: 0, y: 4 }}
                      animate={{ opacity: 1, y: 0 }}
                      transition={{ duration: 0.15 }}
                    >
                      <Card
                        className="cursor-pointer transition-all duration-150 hover:-translate-y-0.5 hover:shadow-md"
                        onClick={() =>
                          navigate(
                            `/projects/${name}/${repo.type}/${repo.name}`,
                          )
                        }
                      >
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
                    </motion.div>
                  ))}
                </div>
              )}
            </div>
          </TabsContent>
        ))}
      </Tabs>

      {/* Create Repo Dialog */}
      <Dialog open={dialogOpen} onOpenChange={setDialogOpen}>
        <DialogContent>
          <form onSubmit={handleCreateRepo}>
            <DialogHeader>
              <DialogTitle>Create Repository</DialogTitle>
            </DialogHeader>
            <div className="space-y-4 py-4">
              {createError && (
                <div className="rounded-md bg-destructive/10 p-3 text-sm text-destructive">
                  {createError}
                </div>
              )}
              <div className="space-y-2">
                <Label htmlFor="repo-type">Type</Label>
                <Select
                  value={repoType}
                  onValueChange={(val) => setRepoType(val as RepoType)}
                >
                  <SelectTrigger id="repo-type">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {REPO_TYPES.map((rt) => (
                      <SelectItem key={rt.value} value={rt.value}>
                        {rt.label}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
              <div className="space-y-2">
                <Label htmlFor="repo-name">Repository Name</Label>
                <Input
                  id="repo-name"
                  value={repoName}
                  onChange={(e) => setRepoName(e.target.value)}
                  placeholder="my-repo"
                  required
                  autoFocus
                />
              </div>
            </div>
            <DialogFooter>
              <Button
                type="submit"
                disabled={createRepo.isPending || !repoName.trim()}
              >
                {createRepo.isPending ? 'Creating...' : 'Create Repository'}
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>
    </div>
  );
}
