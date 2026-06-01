/**
 * Projects list page.
 * Project table with member/repo counts, empty state, create dialog.
 * Migrated from a card grid to a sticky-first-column table layout so
 * SkeletonTable adoption, overflow-x-auto horizontal scroll containment,
 * and sticky-first-column behavior all land on a canonical user-facing
 * surface.
 */

import { useEffect, useState, type FormEvent } from 'react';
import { useNavigate, useSearchParams } from 'react-router-dom';
import { Plus, FolderKanban } from 'lucide-react';
import { toast } from 'sonner';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { Textarea } from '@/components/ui/textarea';
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogFooter,
  DialogTrigger,
} from '@/components/ui/dialog';
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table';
import { SkeletonTable } from '@/components/common/SkeletonTable';
import { EmptyState } from '@/components/common/EmptyState';
import { useProjects, useCreateProject } from '@/api/queries';
import { formatBytes, formatDate } from '@/lib/format';
import {
  envelopeFromError,
  fieldErrorsFromEnvelope,
  type ApiErrorEnvelope,
} from '@/api/client';
import { ErrorEnvelopeRenderer } from '@/components/common/ErrorEnvelope';

export function ProjectsPage() {
  const navigate = useNavigate();
  const [searchParams, setSearchParams] = useSearchParams();
  const { data, isLoading } = useProjects();
  const createProject = useCreateProject();

  const [dialogOpen, setDialogOpen] = useState(false);
  const [name, setName] = useState('');
  const [description, setDescription] = useState('');
  const [errorEnvelope, setErrorEnvelope] = useState<ApiErrorEnvelope | null>(null);
  const fieldErrors = fieldErrorsFromEnvelope(errorEnvelope);

  // Open create dialog when arriving from dashboard with ?create=1.
  useEffect(() => {
    if (searchParams.get('create') === '1') {
      setDialogOpen(true);
      const next = new URLSearchParams(searchParams);
      next.delete('create');
      setSearchParams(next, { replace: true });
    }
  }, [searchParams, setSearchParams]);

  const projects = data?.items ?? [];

  const handleCreate = async (e: FormEvent) => {
    e.preventDefault();
    setErrorEnvelope(null);
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
      setErrorEnvelope(envelopeFromError(err, 'Failed to create project.'));
    }
  };

  return (
    <div className="space-y-6">
      {/* Header */}
      <div className="flex items-center justify-between">
        <h1 className="text-[28px] font-semibold leading-tight">Projects</h1>
        <Dialog
          open={dialogOpen}
          onOpenChange={(open) => {
            setDialogOpen(open);
            if (!open) {
              // Reset form + error banner when the dialog is dismissed
              // (Esc / overlay click / Cancel). Without this, reopening after
              // a rejected submit shows the stale name + stale error.
              setName('');
              setDescription('');
              setErrorEnvelope(null);
            }
          }}
        >
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
                {errorEnvelope && (
                  <ErrorEnvelopeRenderer
                    envelope={errorEnvelope}
                    onRetry={() => handleCreate(new Event('submit') as unknown as FormEvent)}
                  />
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
                    aria-invalid={!!fieldErrors['name'] || !!fieldErrors['project-name'] || undefined}
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

      {/* Loading / empty / populated */}
      {isLoading ? (
        <SkeletonTable
          rows={6}
          columns={6}
          widths={['w-40', 'w-full', 'w-20', 'w-20', 'w-24', 'w-32']}
        />
      ) : projects.length === 0 ? (
        <EmptyState
          icon={FolderKanban}
          title="No projects yet"
          description="Create your first project to start hosting artifacts."
          primaryCTA={{
            label: 'Create project',
            onClick: () => setDialogOpen(true),
          }}
        />
      ) : (
        /*
         * Admin-table pattern: outer overflow-x-auto wrapper
         * contains horizontal scroll INSIDE the table rather than pushing
         * the whole page. First TableHead + first TableCell per row
         * carry `sticky left-0 z-10 bg-card` so the project name stays
         * visible while the operator scans numeric columns on a
         * 1366×768 laptop. Canonical reference implementation — mirrored
         * across admin pages with 6+ columns.
         */
        <div className="overflow-x-auto rounded-lg border">
          <Table className="min-w-full">
            <TableHeader>
              <TableRow>
                <TableHead className="sticky left-0 z-10 bg-card">Name</TableHead>
                <TableHead>Description</TableHead>
                <TableHead className="text-right">Members</TableHead>
                <TableHead className="text-right">Repos</TableHead>
                <TableHead className="text-right">Size</TableHead>
                <TableHead>Created</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {projects.map((project) => (
                <TableRow
                  key={project.id}
                  className="cursor-pointer"
                  onClick={() => navigate(`/projects/${project.name}`)}
                >
                  <TableCell className="sticky left-0 z-10 bg-card font-medium">
                    {project.name}
                  </TableCell>
                  <TableCell className="max-w-md truncate text-sm text-muted-foreground">
                    {project.description_md || '—'}
                  </TableCell>
                  <TableCell className="text-right tabular-nums">
                    {project.member_count}
                  </TableCell>
                  <TableCell className="text-right tabular-nums">
                    {project.repo_count}
                  </TableCell>
                  <TableCell className="text-right tabular-nums text-muted-foreground">
                    {formatBytes(project.size_bytes)}
                  </TableCell>
                  <TableCell className="text-xs text-muted-foreground">
                    {formatDate(project.created_at)}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      )}
    </div>
  );
}
