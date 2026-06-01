import { useState } from 'react';
import { KeyRound, Trash2 } from 'lucide-react';
import { toast } from 'sonner';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { OneTimeReveal } from '@/components/common/OneTimeReveal';
import { formatDate } from '@/lib/format';
import {
  useCreateProjectAPIKey,
  useProjectAPIKeys,
  useRevokeProjectAPIKey,
} from '@/api/queries';
import { useRoleFor } from '@/hooks/useAuth';
import type { APIKey } from '@/api/types';

export interface ProjectAPIKeysCardProps {
  projectName: string;
}

/**
 * Project-scoped API keys (omr_p_*) — pipelines that publish on behalf of a
 * project use these instead of a user's personal token. Mirrors the user
 * APIKeysSection on ProfilePage but lives on the project Overview tab so the
 * pipeline owner doesn't need to leave the project context to mint a token.
 *
 * The plaintext secret is shown ONCE in the OneTimeReveal dialog after
 * create — never on the list view, never in the audit log payload.
 */
export function ProjectAPIKeysCard({ projectName }: ProjectAPIKeysCardProps) {
  const { data, isLoading } = useProjectAPIKeys(projectName);
  const createKey = useCreateProjectAPIKey(projectName);
  const revokeKey = useRevokeProjectAPIKey(projectName);
  const myRole = useRoleFor(projectName);
  const isMaintainer = myRole === 'maintainer';

  const [showCreate, setShowCreate] = useState(false);
  const [keyName, setKeyName] = useState('');
  const [revealSecret, setRevealSecret] = useState('');
  const [showReveal, setShowReveal] = useState(false);
  const [revokeTarget, setRevokeTarget] = useState<APIKey | null>(null);

  const keys = data ?? [];

  const handleCreate = async () => {
    const name = keyName.trim();
    if (!name) return;
    try {
      const result = await createKey.mutateAsync({ name });
      setShowCreate(false);
      setKeyName('');
      setRevealSecret(result.secret);
      setShowReveal(true);
    } catch {
      toast.error('Failed to create project API key.');
    }
  };

  const handleRevoke = async () => {
    if (!revokeTarget) return;
    try {
      await revokeKey.mutateAsync(revokeTarget.id);
      toast.success('Project API key revoked.');
      setRevokeTarget(null);
    } catch {
      toast.error('Failed to revoke project API key.');
    }
  };

  return (
    <Card>
      <CardHeader>
        <div className="flex items-center justify-between">
          <div className="flex items-center gap-2">
            <KeyRound className="size-4 text-muted-foreground" />
            <CardTitle>Project API Keys</CardTitle>
          </div>
          {isMaintainer && (
            <Button
              variant="outline"
              size="sm"
              onClick={() => {
                setKeyName('');
                setShowCreate(true);
              }}
            >
              Mint Token
            </Button>
          )}
        </div>
      </CardHeader>
      <CardContent>
        {isLoading ? (
          <p className="text-sm text-muted-foreground">Loading…</p>
        ) : keys.length === 0 ? (
          <p className="text-sm text-muted-foreground">
            No project tokens yet. Mint one for a CI pipeline that publishes
            on behalf of this project — the token starts with{' '}
            <code className="font-mono">omr_p_</code>.
          </p>
        ) : (
          <div className="space-y-2">
            {keys.map((k) => (
              <div
                key={k.id}
                className="flex items-center justify-between gap-3 text-sm"
              >
                <div className="min-w-0">
                  <p className="truncate font-semibold">{k.name}</p>
                  <p className="text-xs text-muted-foreground">
                    <code className="font-mono">{k.prefix}…</code> · created{' '}
                    {formatDate(k.created_at)}
                    {k.last_used_at
                      ? ` · last used ${formatDate(k.last_used_at)}`
                      : ' · never used'}
                  </p>
                </div>
                {isMaintainer && (
                  <Button
                    variant="ghost"
                    size="sm"
                    className="text-destructive hover:bg-destructive/10"
                    onClick={() => setRevokeTarget(k)}
                    aria-label={`Revoke ${k.name}`}
                    title="Revoke token"
                  >
                    <Trash2 className="size-4" />
                  </Button>
                )}
              </div>
            ))}
          </div>
        )}
      </CardContent>

      <Dialog open={showCreate} onOpenChange={setShowCreate}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Mint project API key</DialogTitle>
            <DialogDescription>
              Names help identify which pipeline a token belongs to. The
              plaintext secret is shown only once after creation.
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-2 py-2">
            <Label htmlFor="proj-key-name">Name</Label>
            <Input
              id="proj-key-name"
              value={keyName}
              onChange={(e) => setKeyName(e.target.value)}
              placeholder="e.g. ci-publisher"
              autoFocus
            />
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setShowCreate(false)}>
              Cancel
            </Button>
            <Button
              onClick={handleCreate}
              disabled={!keyName.trim() || createKey.isPending}
            >
              {createKey.isPending ? 'Minting…' : 'Mint Token'}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <OneTimeReveal
        open={showReveal}
        onOpenChange={setShowReveal}
        title="Your Project API Key"
        secret={revealSecret}
        warningText="This token will not be shown again. Copy it now."
      />

      <Dialog
        open={!!revokeTarget}
        onOpenChange={(open) => !open && setRevokeTarget(null)}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Revoke {revokeTarget?.name}?</DialogTitle>
            <DialogDescription>
              The token will stop working immediately. Continue?
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
              {revokeKey.isPending ? 'Revoking…' : 'Revoke'}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </Card>
  );
}
