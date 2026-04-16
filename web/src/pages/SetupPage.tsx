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
import { Navigate, useNavigate } from 'react-router-dom';
import { motion } from 'framer-motion';
import { useQueryClient } from '@tanstack/react-query';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { useSetupStatus, useSetupSuperAdmin } from '@/api/queries';
import { ApiError } from '@/api/client';
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
  const [error, setError] = useState('');
  // After a successful submission we want the explicit navigate('/login',
  // {state: {setupDone: true}}) to take effect. Previously, the mutation's
  // onSuccess flipped needs_setup to false which re-rendered this component
  // into the <Navigate to="/login"> short-circuit BEFORE handleSubmit's
  // navigate() ran — and <Navigate> doesn't carry state, so the success
  // banner never showed on /login (F-6). Tracking the submission locally
  // suppresses the short-circuit for the winning tab.
  const [submitted, setSubmitted] = useState(false);

  if (isLoading) {
    return (
      <div className="flex min-h-screen items-center justify-center bg-background">
        <p className="text-muted-foreground">Checking setup status…</p>
      </div>
    );
  }

  // If setup is already done, bounce to /login rather than showing a form
  // that would just 409. Handles the case where an admin navigates to /setup
  // manually after the install is live. `submitted` opts the winning tab
  // out so the post-mutation navigate() with state takes effect.
  if (!submitted && status && !status.needs_setup) {
    return <Navigate to="/login" replace />;
  }

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault();
    setError('');

    if (password.length < 8) {
      setError('Password must be at least 8 characters.');
      return;
    }
    if (password !== confirm) {
      setError('Passwords do not match.');
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
          setError(err.detail || 'One of the fields is invalid.');
        } else {
          setError('Unable to reach the server. Please try again.');
        }
      } else {
        setError('Unable to reach the server. Please try again.');
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
              {error && (
                <div className="rounded-md bg-destructive/10 p-3 text-sm text-destructive">
                  {error}
                </div>
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
