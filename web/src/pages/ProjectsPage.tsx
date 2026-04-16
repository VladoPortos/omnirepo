/**
 * Projects list page per D-04.
 * Project cards with member/repo counts, empty state, create dialog.
 */

import { useState, type FormEvent } from 'react';
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
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogFooter,
  DialogTrigger,
} from '@/components/ui/dialog';
import { useProjects, useCreateProject } from '@/api/queries';
import { formatBytes, formatDate } from '@/lib/format';
import { ApiError } from '@/api/client';

const cardVariants = {
  hidden: { opacity: 0, y: 12 },
  visible: (i: number) => ({
    opacity: 1,
    y: 0,
    transition: { delay: i * 0.04, duration: 0.2, ease: 'easeOut' as const },
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

  const handleCreate = async (e: FormEvent) => {
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
    } catch (err) {
      if (err instanceof ApiError) {
        setError(err.detail);
      } else {
        setError('Failed to create project.');
      }
    }
  };

  return (
    <div className="space-y-6">
      {/* Header */}
      <div className="flex items-center justify-between">
        <h1 className="text-[28px] font-semibold leading-tight">Projects</h1>
        <Dialog open={dialogOpen} onOpenChange={setDialogOpen}>
          <DialogTrigger render={<Button size="sm" />}>
            <Plus className="mr-1.5 size-4" />
            Create Project
          </DialogTrigger>
          <DialogContent>
            <form onSubmit={handleCreate}>
              <DialogHeader>
                <DialogTitle>Create Project</DialogTitle>
              </DialogHeader>
              <div className="space-y-4 py-4">
                {error && (
                  <div className="rounded-md bg-destructive/10 p-3 text-sm text-destructive">
                    {error}
                  </div>
                )}
                <div className="space-y-2">
                  <Label htmlFor="project-name">Project Name</Label>
                  <Input
                    id="project-name"
                    value={name}
                    onChange={(e) => setName(e.target.value)}
                    placeholder="my-project"
                    required
                    autoFocus
                  />
                  <p className="text-xs text-muted-foreground">
                    URL-safe slug (lowercase letters, numbers, hyphens)
                  </p>
                </div>
                <div className="space-y-2">
                  <Label htmlFor="project-desc">Description (optional)</Label>
                  <Textarea
                    id="project-desc"
                    value={description}
                    onChange={(e) => setDescription(e.target.value)}
                    placeholder="Brief description of this project..."
                    rows={3}
                  />
                </div>
              </div>
              <DialogFooter>
                <Button
                  type="submit"
                  disabled={createProject.isPending || !name.trim()}
                >
                  {createProject.isPending ? 'Creating...' : 'Create Project'}
                </Button>
              </DialogFooter>
            </form>
          </DialogContent>
        </Dialog>
      </div>

      {/* Projects grid */}
      {isLoading ? (
        <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
          {Array.from({ length: 6 }).map((_, i) => (
            <Card key={i}>
              <CardHeader>
                <Skeleton className="h-5 w-32" />
              </CardHeader>
              <CardContent>
                <Skeleton className="h-4 w-full" />
                <Skeleton className="mt-2 h-4 w-24" />
              </CardContent>
            </Card>
          ))}
        </div>
      ) : projects.length === 0 ? (
        <div className="flex flex-col items-center justify-center rounded-lg border border-dashed p-12 text-center">
          <FolderGit2 className="size-12 text-muted-foreground/50" />
          <h2 className="mt-4 text-lg font-semibold">No projects yet</h2>
          <p className="mt-2 max-w-md text-sm text-muted-foreground">
            Create your first project to start hosting artifacts.
          </p>
          <Button
            className="mt-6"
            onClick={() => setDialogOpen(true)}
          >
            <Plus className="mr-1.5 size-4" />
            Create Project
          </Button>
        </div>
      ) : (
        <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
          {projects.map((project, i) => (
            <motion.div
              key={project.id}
              custom={i}
              initial="hidden"
              animate="visible"
              variants={cardVariants}
            >
              <Card
                className="cursor-pointer transition-all duration-150 hover:-translate-y-0.5 hover:shadow-md"
                onClick={() => navigate(`/projects/${project.name}`)}
              >
                <CardHeader>
                  <CardTitle className="text-base">{project.name}</CardTitle>
                </CardHeader>
                <CardContent>
                  {project.description_md && (
                    <p className="mb-3 line-clamp-2 text-sm text-muted-foreground">
                      {project.description_md}
                    </p>
                  )}
                  <div className="flex items-center gap-4 text-xs text-muted-foreground">
                    <span className="inline-flex items-center gap-1">
                      <Users className="size-3.5" />
                      {project.member_count} member{project.member_count !== 1 ? 's' : ''}
                    </span>
                    <span className="inline-flex items-center gap-1">
                      <FolderGit2 className="size-3.5" />
                      {project.repo_count} repo{project.repo_count !== 1 ? 's' : ''}
                    </span>
                    <span>{formatBytes(project.size_bytes)}</span>
                  </div>
                  <p className="mt-2 text-xs text-muted-foreground">
                    Created {formatDate(project.created_at)}
                  </p>
                </CardContent>
              </Card>
            </motion.div>
          ))}
        </div>
      )}
    </div>
  );
}
