/**
 * Shared layout for all repo detail pages.
 * Provides breadcrumb, header, tabs (Content / Scan Results / Settings),
 * snippet panel, and settings form.
 */

import { useState, type ReactNode } from 'react';
import { useParams } from 'react-router-dom';
import { Settings, Trash2, AlertTriangle } from 'lucide-react';
import { toast } from 'sonner';
import { Tabs, TabsList, TabsTrigger, TabsContent } from '@/components/ui/tabs';
import { Button } from '@/components/ui/button';
import { Switch } from '@/components/ui/switch';
import { Label } from '@/components/ui/label';
import { Skeleton } from '@/components/ui/skeleton';
import { Separator } from '@/components/ui/separator';
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
  DialogFooter,
} from '@/components/ui/dialog';
import { SnippetPanel } from '@/components/common/SnippetPanel';
import { usePatchRepo, useDeleteRepo } from '@/api/queries';
import { formatBytes } from '@/lib/format';
import { ApiError } from '@/api/client';
import type { Repo, RepoType, BlockSeverity } from '@/api/types';

interface RepoPageLayoutProps {
  repo: Repo;
  children: ReactNode;
  scanContent?: ReactNode;
}

const TYPE_LABELS: Record<RepoType, string> = {
  docker: 'Docker',
  rpm: 'RPM',
  deb: 'APT',
  pypi: 'PyPI',
  helm: 'Helm',
  git: 'Git',
  raw: 'RAW',
  s3: 'S3',
};

export function RepoPageLayout({ repo, children, scanContent }: RepoPageLayoutProps) {
  const { name: projectName } = useParams<{ name: string }>();
  const [tab, setTab] = useState('content');
  const [wipeOpen, setWipeOpen] = useState(false);
  const [deleteOpen, setDeleteOpen] = useState(false);
  const patchRepo = usePatchRepo();
  const deleteRepo = useDeleteRepo();

  const hostname = window.location.host;
  const typeLabel = TYPE_LABELS[repo.type] ?? repo.type;

  const handleToggle = (field: 'auto_scan' | 'public_read', value: boolean) => {
    patchRepo.mutate(
      { projectName: projectName!, repoType: repo.type, repoName: repo.name, data: { [field]: value } },
      {
        onSuccess: () => toast.success(`${field === 'auto_scan' ? 'Auto-scan' : 'Public read'} updated.`),
        onError: (err) => {
          const msg = err instanceof ApiError ? err.detail : 'Unknown error';
          toast.error(`Failed to update: ${msg}`);
        },
      },
    );
  };

  const handleBlockSeverity = (value: BlockSeverity) => {
    patchRepo.mutate(
      { projectName: projectName!, repoType: repo.type, repoName: repo.name, data: { block_on_severity: value } },
      {
        onSuccess: () => toast.success('Block severity updated.'),
        onError: (err) => {
          const msg = err instanceof ApiError ? err.detail : 'Unknown error';
          toast.error(`Failed to update: ${msg}`);
        },
      },
    );
  };

  return (
    <div className="space-y-6">
      {/* Header */}
      <div className="flex flex-wrap items-center justify-between gap-4">
        <div>
          <h1 className="text-2xl font-semibold">{repo.name}</h1>
          <p className="text-sm text-muted-foreground">
            {typeLabel} repository &middot; {formatBytes(repo.size_bytes)}
          </p>
        </div>
        <SnippetPanel
          repoType={repo.type}
          projectName={projectName!}
          repoName={repo.name}
          hostname={hostname}
        />
      </div>

      {/* Tabs */}
      <Tabs defaultValue="content" value={tab} onValueChange={setTab}>
        <TabsList>
          <TabsTrigger value="content">Content</TabsTrigger>
          <TabsTrigger value="scan">Scan Results</TabsTrigger>
          <TabsTrigger value="settings">
            <Settings className="mr-1 size-3.5" />
            Settings
          </TabsTrigger>
        </TabsList>

        <TabsContent value="content">{children}</TabsContent>

        <TabsContent value="scan">
          {scanContent ?? (
            <p className="py-8 text-center text-sm text-muted-foreground">
              Scan results will be displayed here.
            </p>
          )}
        </TabsContent>

        <TabsContent value="settings">
          <div className="max-w-2xl space-y-6 py-4">
            {/* Auto-scan toggle */}
            <div className="flex items-center justify-between">
              <div>
                <Label className="text-sm font-medium">Auto-scan uploads</Label>
                <p className="text-xs text-muted-foreground">
                  Automatically scan new artifacts for vulnerabilities on upload.
                </p>
              </div>
              <Switch
                checked={repo.auto_scan}
                onCheckedChange={(checked: boolean) => handleToggle('auto_scan', checked)}
              />
            </div>

            <Separator />

            {/* Block severity select */}
            <div className="space-y-2">
              <Label className="text-sm font-medium">Block on severity</Label>
              <p className="text-xs text-muted-foreground">
                Prevent pulling artifacts with vulnerabilities at or above this severity.
              </p>
              <div className="flex gap-2">
                {(['none', 'low', 'medium', 'high', 'critical'] as BlockSeverity[]).map((sev) => (
                  <Button
                    key={sev}
                    variant={repo.block_on_severity === sev ? 'default' : 'outline'}
                    size="sm"
                    onClick={() => handleBlockSeverity(sev)}
                  >
                    {sev === 'none' ? 'None' : sev.charAt(0).toUpperCase() + sev.slice(1)}
                  </Button>
                ))}
              </div>
            </div>

            <Separator />

            {/* Public read toggle */}
            <div className="flex items-center justify-between">
              <div>
                <Label className="text-sm font-medium">Public read access</Label>
                <p className="text-xs text-muted-foreground">
                  Allow unauthenticated read access to this repository.
                </p>
              </div>
              <Switch
                checked={repo.public_read}
                onCheckedChange={(checked: boolean) => handleToggle('public_read', checked)}
              />
            </div>

            <Separator />

            {/* Danger zone */}
            <div className="space-y-4 rounded-lg border border-destructive/30 p-4">
              <h3 className="text-sm font-semibold text-destructive">Danger Zone</h3>
              <div className="flex items-center justify-between">
                <div>
                  <p className="text-sm font-medium">Wipe all contents</p>
                  <p className="text-xs text-muted-foreground">
                    Remove all artifacts. Repository structure is preserved.
                  </p>
                </div>
                <Button variant="destructive" size="sm" onClick={() => setWipeOpen(true)}>
                  <Trash2 className="mr-1.5 size-4" />
                  Wipe
                </Button>
              </div>
              <div className="flex items-center justify-between">
                <div>
                  <p className="text-sm font-medium">Delete repository</p>
                  <p className="text-xs text-muted-foreground">
                    Permanently delete this repository and all data.
                  </p>
                </div>
                <Button variant="destructive" size="sm" onClick={() => setDeleteOpen(true)}>
                  <Trash2 className="mr-1.5 size-4" />
                  Delete
                </Button>
              </div>
            </div>
          </div>
        </TabsContent>
      </Tabs>

      {/* Wipe confirmation dialog */}
      <Dialog open={wipeOpen} onOpenChange={setWipeOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Wipe Repository Contents</DialogTitle>
            <DialogDescription>
              This will remove all artifacts from <strong>{repo.name}</strong>. This action moves
              content to trash and can be recovered within the retention period.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setWipeOpen(false)}>
              Cancel
            </Button>
            <Button
              variant="destructive"
              onClick={() => {
                toast.info('Wipe requested (API not yet connected).');
                setWipeOpen(false);
              }}
            >
              <AlertTriangle className="mr-1.5 size-4" />
              Wipe Contents
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Delete confirmation dialog */}
      <Dialog open={deleteOpen} onOpenChange={setDeleteOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete Repository</DialogTitle>
            <DialogDescription>
              Are you sure you want to delete <strong>{repo.name}</strong>? This action cannot be
              undone after the trash retention period expires.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setDeleteOpen(false)}>
              Cancel
            </Button>
            <Button
              variant="destructive"
              onClick={() => {
                deleteRepo.mutate(
                  { projectName: projectName!, repoType: repo.type, repoName: repo.name },
                  {
                    onSuccess: () => {
                      toast.success('Repository deleted.');
                      window.history.back();
                    },
                    onError: (err) => {
                      const msg = err instanceof ApiError ? err.detail : 'Unknown error';
                      toast.error(`Delete failed: ${msg}`);
                    },
                  },
                );
                setDeleteOpen(false);
              }}
            >
              <Trash2 className="mr-1.5 size-4" />
              Delete Repository
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}

export function RepoSkeleton() {
  return (
    <div className="space-y-6">
      <Skeleton className="h-5 w-64" />
      <div className="space-y-2">
        <Skeleton className="h-8 w-48" />
        <Skeleton className="h-4 w-32" />
      </div>
      <Skeleton className="h-8 w-full max-w-sm" />
      <Skeleton className="h-64 w-full" />
    </div>
  );
}
