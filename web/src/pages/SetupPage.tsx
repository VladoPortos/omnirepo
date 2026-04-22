/**
 * First-run super-admin creation page.
 *
 * Shown whenever GET /api/v1/setup/status returns needs_setup=true (i.e. the
 * users table is empty). The SetupGuard in App.tsx forces navigation here so
 * operators never hit a confusing login screen on a fresh install.
 *
 * After a successful POST /setup/superadmin the user is redirected to /login
 * with a banner flag carried in location state.
 */

import { useState, type FormEvent } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import { motion } from 'framer-motion';
import { useQueryClient } from '@tanstack/react-query';
import { CheckCircle2 } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { useSetupStatus, useSetupSuperAdmin } from '@/api/queries';
import {
  ApiError,
  localEnvelope,
  fieldErrorsFromEnvelope,
  type ApiErrorEnvelope,
} from '@/api/client';
import { ErrorEnvelopeRenderer } from '@/components/common/ErrorEnvelope';
import type { SetupStatusResponse } from '@/api/types';

export function SetupPage() {
  const navigate = useNavigate();
  const qc = useQueryClient();
  const { data: status, isLoading } = useSetupStatus();
  const setup = useSetupSuperAdmin();

  const [login, setLogin] = useState('');
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [confirm, setConfirm] = useState('');
  const [errorEnvelope, setErrorEnvelope] = useState<ApiErrorEnvelope | null>(null);
  // After a successful submission we want the explicit navigate('/login',
  // {state: {setupDone: true}}) to take effect. Previously, the mutation's
  // onSuccess flipped needs_setup to false which re-rendered this component
  // into the <Navigate to="/login"> short-circuit BEFORE handleSubmit's
  // navigate() ran — and <Navigate> doesn't carry state, so the success
  // banner never showed on /login (F-6). Tracking the submission locally
  // suppresses the short-circuit for the winning tab.
  const [submitted, setSubmitted] = useState(false);

  // fieldErrors = {inputId -> message} derived from the current envelope.
  // Drives aria-invalid on the offending <Input> so the "Check the
  // highlighted field." hint from ErrorEnvelopeRenderer has something
  // visible to highlight. Re-computed each render — cheap, and avoids
  // staleness when the envelope is cleared between submissions.
  const fieldErrors = fieldErrorsFromEnvelope(errorEnvelope);

  if (isLoading) {
    return (
      <div className="flex min-h-screen items-center justify-center bg-background">
        <p className="text-muted-foreground">Checking setup status…</p>
      </div>
    );
  }

  // F-T9: when setup is already done, render an explicit "Setup complete"
  // screen instead of silently bouncing to /login. The old <Navigate>
  // short-circuit made it look like /setup was broken — the form would flash
  // briefly under some cache-warming sequences, or confused admins who
  // navigated here manually. `submitted` opts the winning tab out so the
  // post-mutation navigate() with {state:setupDone} reaches /login cleanly.
  if (!submitted && status && !status.needs_setup) {
    return (
      <div className="flex min-h-screen items-center justify-center bg-background p-4">
        <motion.div
          initial={{ opacity: 0, scale: 0.95 }}
          animate={{ opacity: 1, scale: 1 }}
          transition={{ duration: 0.15, ease: 'easeOut' }}
          className="w-full max-w-md"
        >
          <Card className="w-full max-w-md">
            <CardHeader className="text-center">
              <div className="mx-auto flex size-12 items-center justify-center rounded-lg bg-emerald-500/15 text-emerald-600 mb-4">
                <CheckCircle2 className="size-6" />
              </div>
              <CardTitle className="text-2xl">Setup complete</CardTitle>
              <p className="text-muted-foreground text-sm mt-2">
                A super-admin account already exists. Sign in to continue.
              </p>
            </CardHeader>
            <CardContent>
              <Button
                className="w-full"
                nativeButton={false}
                render={<Link to="/login">Go to sign in</Link>}
              />
            </CardContent>
          </Card>
        </motion.div>
      </div>
    );
  }

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault();
    setErrorEnvelope(null);

    if (password.length < 8) {
      setErrorEnvelope(
        localEnvelope('Password must be at least 8 characters.', {
          details: { field: 'setup-password' },
        }),
      );
      return;
    }
    if (password !== confirm) {
      setErrorEnvelope(
        localEnvelope('Passwords do not match.', {
          details: { field: 'setup-confirm' },
        }),
      );
      return;
    }

    // Mark submitted BEFORE the mutation so the Navigate short-circuit
    // can't fire between onSuccess (flips cache to needs_setup=false) and
    // our navigate('/login', {state}) call.
    setSubmitted(true);
    try {
      await setup.mutateAsync({ login, email, password });
      navigate('/login', { state: { setupDone: true } });
    } catch (err) {
      if (err instanceof ApiError) {
        if (err.status === 409) {
          // Another operator finished setup first. Flip the cached status so
          // the SetupGuard (staleTime: Infinity) stops forcing /setup, then
          // send this tab to the login page instead of showing a dead error.
          qc.setQueryData<SetupStatusResponse>(['setup', 'status'], { needs_setup: false });
          navigate('/login', { replace: true });
          return;
        } else if (err.status === 422) {
          setErrorEnvelope(err.envelope);
        } else {
          setErrorEnvelope(
            localEnvelope('Unable to reach the server. Please try again.', {
              class: 'transient',
              code: 'ui.network',
            }),
          );
        }
      } else {
        setErrorEnvelope(
          localEnvelope('Unable to reach the server. Please try again.', {
            class: 'transient',
            code: 'ui.network',
          }),
        );
      }
      // Mutation failed — re-allow the Navigate short-circuit for any future
      // re-render that sees needs_setup=false (e.g. another operator won the
      // race between retries).
      setSubmitted(false);
    }
  };

  return (
    <div className="flex min-h-screen items-center justify-center bg-background p-4">
      <motion.div
        initial={{ opacity: 0, scale: 0.95 }}
        animate={{ opacity: 1, scale: 1 }}
        transition={{ duration: 0.15, ease: 'easeOut' }}
        className="w-full max-w-md"
      >
        <Card className="w-full max-w-md">
          <CardHeader className="text-center">
            <div className="mx-auto flex size-12 items-center justify-center rounded-lg bg-primary text-primary-foreground mb-4">
              <span className="text-xl font-semibold">O</span>
            </div>
            <CardTitle className="text-2xl">Welcome to OmniRepo</CardTitle>
            <p className="text-muted-foreground text-sm mt-2">
              Create the first super-admin account to finish setup.
            </p>
          </CardHeader>
          <CardContent>
            <form onSubmit={handleSubmit} className="space-y-4">
              {errorEnvelope && (
                <ErrorEnvelopeRenderer envelope={errorEnvelope} />
              )}
              <div className="space-y-2">
                <Label htmlFor="setup-login">Login</Label>
                <Input
                  id="setup-login"
                  type="text"
                  value={login}
                  onChange={(e) => setLogin(e.target.value)}
                  placeholder="admin"
                  autoComplete="username"
                  autoFocus
                  required
                  aria-invalid={
                    !!fieldErrors['setup-login'] ||
                    !!fieldErrors['login'] ||
                    undefined
                  }
                />
              </div>
              <div className="space-y-2">
                <Label htmlFor="setup-email">Email</Label>
                <Input
                  id="setup-email"
                  type="email"
                  value={email}
                  onChange={(e) => setEmail(e.target.value)}
                  placeholder="admin@example.com"
                  autoComplete="email"
                  required
                  aria-invalid={
                    !!fieldErrors['setup-email'] ||
                    !!fieldErrors['email'] ||
                    undefined
                  }
                />
              </div>
              <div className="space-y-2">
                <Label htmlFor="setup-password">Password</Label>
                <Input
                  id="setup-password"
                  type="password"
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                  placeholder="At least 8 characters"
                  autoComplete="new-password"
                  minLength={8}
                  required
                  aria-invalid={!!fieldErrors['setup-password'] || undefined}
                />
              </div>
              <div className="space-y-2">
                <Label htmlFor="setup-confirm">Confirm password</Label>
                <Input
                  id="setup-confirm"
                  type="password"
                  value={confirm}
                  onChange={(e) => setConfirm(e.target.value)}
                  placeholder="Re-enter password"
                  autoComplete="new-password"
                  minLength={8}
                  required
                  aria-invalid={!!fieldErrors['setup-confirm'] || undefined}
                />
              </div>
              <Button
                type="submit"
                className="w-full"
                disabled={setup.isPending}
              >
                {setup.isPending ? 'Creating account…' : 'Create super-admin'}
              </Button>
            </form>
          </CardContent>
        </Card>
      </motion.div>
    </div>
  );
}
