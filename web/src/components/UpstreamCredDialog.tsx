/**
 * UpstreamCredDialog.
 *
 * Add/Edit modal for a project-scoped upstream credential. Two modes:
 *
 *   create — empty form; password and token inputs are accepted
 *             (backend requires at least one).
 *   edit    — host/kind/username prefilled from the passed `cred`;
 *             password and token inputs start BLANK and submit as
 *             omitted keys when still blank on save, so the backend
 *             preserves the existing secret.
 *
 * Security properties:
 *   - The component never reads password/token off the `cred` prop
 *     (the UpstreamCred type omits them anyway — defense in depth at
 *     the type layer). The prefill effect only touches
 *     host / kind / username.
 *   - Password and token <Input>s use type="password" + autocomplete=
 *     "new-password" so browsers don't pre-fill nor echo.
 *   - Mutation error (e.g. backend `password_or_token_required` or
 *     `upstream cred already exists for host+kind`) renders via
 *     ErrorEnvelopeRenderer inline.
 *
 * Wire contract — see internal/api/upstream_creds.go:
 *   POST   /api/v1/projects/{name}/upstream-creds/
 *   PATCH  /api/v1/projects/{name}/upstream-creds/{id}
 */

'use client';
import { useEffect, useState } from 'react';
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
import { Button } from '@/components/ui/button';
import { ErrorEnvelopeRenderer } from '@/components/common/ErrorEnvelope';
import { envelopeFromError, type ApiErrorEnvelope } from '@/api/client';
import {
  useCreateUpstreamCred,
  usePatchUpstreamCred,
} from '@/api/queries';
import type {
  UpstreamCred,
  UpstreamCredCreate,
  UpstreamCredKind,
  UpstreamCredPatch,
} from '@/api/types';

export type UpstreamCredDialogMode = 'create' | 'edit';

export interface UpstreamCredDialogProps {
  open: boolean;
  onClose: () => void;
  projectName: string;
  mode: 'create' | 'edit';
  /** Required when `mode === 'edit'`. Passing undefined in edit mode
   *  is a programmer error and renders a disabled save button. */
  cred?: UpstreamCred;
}

/**
 * CRED_KINDS — the five kinds accepted by the backend. Mirrors
 * metadata.ValidCredKinds (internal/metadata/upstream_creds.go).
 * The UI surfaces a single canonical 'apt' entry; the obsolete 'deb'
 * alias was removed.
 */
const CRED_KINDS: { value: UpstreamCredKind; label: string }[] = [
  { value: 'docker', label: 'Docker / OCI registry' },
  { value: 'apt', label: 'APT (deb)' },
  { value: 'rpm', label: 'RPM / YUM' },
  { value: 'pypi', label: 'PyPI' },
  { value: 'helm', label: 'Helm chart repo' },
];

export function UpstreamCredDialog({
  open,
  onClose,
  projectName,
  mode,
  cred,
}: UpstreamCredDialogProps) {
  const [host, setHost] = useState('');
  const [kind, setKind] = useState<UpstreamCredKind>('docker');
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [token, setToken] = useState('');
  const [mutationError, setMutationError] = useState<ApiErrorEnvelope | null>(
    null,
  );

  const createM = useCreateUpstreamCred(projectName);
  // The patch hook takes the id at construction time; in create mode it
  // is never invoked, so passing 0 is safe.
  const patchM = usePatchUpstreamCred(projectName, cred?.id ?? 0);

  // Reset form whenever the dialog opens. In edit mode, prefill from
  // `cred` EXCEPT password + token which stay blank by contract.
  useEffect(() => {
    if (!open) return;
    setMutationError(null);
    createM.reset();
    patchM.reset();
    if (mode === 'edit' && cred) {
      setHost(cred.host);
      // cred.kind is a free-form string on the wire; cast is safe
      // because the backend only accepts values in UpstreamCredKind.
      setKind((cred.kind as UpstreamCredKind) || 'docker');
      setUsername(cred.username ?? '');
      setPassword('');
      setToken('');
    } else {
      setHost('');
      setKind('docker');
      setUsername('');
      setPassword('');
      setToken('');
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, mode, cred?.id]);

  const mutation = mode === 'edit' ? patchM : createM;
  const isPending = mutation.isPending;

  // Submit-disabled rule:
  //   - host + kind are always required
  //   - create mode: the backend enforces at least one of password/token,
  //     so we only require non-empty host to keep the client light (the
  //     413/400 envelope surfaces via mutationError)
  //   - edit mode: we allow leaving everything as-is if only username
  //     is changing (and password/token blank → preserve)
  //   - pending → disabled
  //   - edit mode with missing cred → disabled (guardrail)
  const canSubmit =
    !!host.trim() &&
    !!kind &&
    !isPending &&
    !(mode === 'edit' && !cred);

  const handleSubmit = () => {
    setMutationError(null);
    const trimmedHost = host.trim();
    const trimmedUsername = username.trim();
    if (mode === 'edit' && cred) {
      // Build PATCH body. Omit password/token ENTIRELY when blank so
      // the backend preserves the existing secret. Do NOT
      // send `""` — strip the key.
      const body: UpstreamCredPatch = {
        host: trimmedHost,
        kind,
        username: trimmedUsername,
      };
      if (password) body.password = password;
      if (token) body.token = token;
      patchM.mutate(body, {
        onSuccess: () => onClose(),
        onError: (err) => {
          setMutationError(
            envelopeFromError(err, 'Failed to update credential.'),
          );
        },
      });
      return;
    }
    // create mode
    const body: UpstreamCredCreate = {
      host: trimmedHost,
      kind,
      username: trimmedUsername || undefined,
      password: password || undefined,
      token: token || undefined,
    };
    createM.mutate(body, {
      onSuccess: () => onClose(),
      onError: (err) => {
        setMutationError(
          envelopeFromError(err, 'Failed to add credential.'),
        );
      },
    });
  };

  const title =
    mode === 'edit'
      ? 'Edit credential'
      : 'Add upstream credential';
  const description =
    mode === 'edit'
      ? 'Update the stored upstream credential. Leave password or token blank to keep the existing value.'
      : 'Store a credential so mirror syncs and Docker clones can authenticate against private upstream archives.';

  return (
    <Dialog open={open} onOpenChange={(o: boolean) => (!o ? onClose() : null)}>
      <DialogContent aria-label={title}>
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>{description}</DialogDescription>
        </DialogHeader>

        <div className="space-y-3 py-2">
          <div className="space-y-1.5">
            <Label htmlFor="cred-host">Host</Label>
            <Input
              id="cred-host"
              placeholder="archive.ubuntu.com"
              value={host}
              onChange={(e) => setHost(e.target.value)}
              autoFocus={mode === 'create'}
              required
              autoComplete="off"
            />
          </div>

          <div className="space-y-1.5">
            <Label htmlFor="cred-kind">Kind</Label>
            <select
              id="cred-kind"
              value={kind}
              onChange={(e) => setKind(e.target.value as UpstreamCredKind)}
              className="h-8 w-full rounded-lg border border-input bg-transparent px-2 text-sm outline-none focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50 disabled:opacity-50"
              disabled={isPending}
            >
              {CRED_KINDS.map((k) => (
                <option key={k.value} value={k.value}>
                  {k.label}
                </option>
              ))}
            </select>
          </div>

          <div className="space-y-1.5">
            <Label htmlFor="cred-username">Username (optional)</Label>
            <Input
              id="cred-username"
              placeholder="myuser"
              value={username}
              onChange={(e) => setUsername(e.target.value)}
              autoComplete="off"
            />
          </div>

          <div className="space-y-1.5">
            <Label htmlFor="cred-password">Password</Label>
            <Input
              id="cred-password"
              type="password"
              placeholder={
                mode === 'edit' ? 'Leave blank to keep existing' : ''
              }
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              autoComplete="new-password"
            />
          </div>

          <div className="space-y-1.5">
            <Label htmlFor="cred-token">Token</Label>
            <Input
              id="cred-token"
              type="password"
              placeholder={
                mode === 'edit' ? 'Leave blank to keep existing' : ''
              }
              value={token}
              onChange={(e) => setToken(e.target.value)}
              autoComplete="new-password"
            />
          </div>

          {mode !== 'edit' && (
            <p className="text-xs text-muted-foreground">
              Provide at least one of password or token. The backend
              encrypts secrets at rest and never echoes them back.
            </p>
          )}

          {mutationError ? (
            <ErrorEnvelopeRenderer envelope={mutationError} mode="inline" />
          ) : null}
        </div>

        <DialogFooter>
          <Button variant="outline" onClick={onClose} disabled={isPending}>
            Cancel
          </Button>
          <Button onClick={handleSubmit} disabled={!canSubmit}>
            {mode === 'edit' ? 'Save changes' : 'Add credential'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
