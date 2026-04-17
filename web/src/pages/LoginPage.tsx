/**
 * Login page per D-05.
 * Full-page dark background, centered card (max-w-md).
 * framer-motion card fade+scale-in on mount per D-02.
 */

import { useState, type FormEvent } from 'react';
import { useLocation, useNavigate } from 'react-router-dom';
import { motion } from 'framer-motion';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { useAuth } from '@/hooks/useAuth';
import { ApiError, localEnvelope, type ApiErrorEnvelope } from '@/api/client';
import { ErrorEnvelopeRenderer } from '@/components/common/ErrorEnvelope';

export function LoginPage() {
  const navigate = useNavigate();
  const location = useLocation();
  const setupDone = (location.state as { setupDone?: boolean } | null)?.setupDone ?? false;
  const { login } = useAuth();
  const [loginValue, setLoginValue] = useState('');
  const [password, setPassword] = useState('');
  const [errorEnvelope, setErrorEnvelope] = useState<ApiErrorEnvelope | null>(null);

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault();
    setErrorEnvelope(null);

    try {
      const result = await login.mutateAsync({
        login: loginValue,
        password,
      });
      if (result.must_change_password) {
        navigate('/change-password');
      } else {
        navigate('/');
      }
    } catch (err) {
      if (err instanceof ApiError && (err.status === 401 || err.status === 403)) {
        setErrorEnvelope(
          localEnvelope('Invalid login or password. Please try again.', {
            class: 'permission',
            code: 'auth.invalid_credentials',
          }),
        );
      } else {
        setErrorEnvelope(
          localEnvelope(
            'Unable to reach the server. Check your connection and try again.',
            { class: 'transient', code: 'ui.network' },
          ),
        );
      }
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
            <CardTitle className="text-2xl">Sign in to OmniRepo</CardTitle>
          </CardHeader>
          <CardContent>
            <form onSubmit={handleSubmit} className="space-y-4">
              {setupDone && !errorEnvelope && (
                <div className="rounded-md bg-green-500/10 p-3 text-sm text-green-600 dark:text-green-400">
                  Super-admin account created. Sign in to continue.
                </div>
              )}
              {errorEnvelope && (
                <ErrorEnvelopeRenderer envelope={errorEnvelope} />
              )}
              <div className="space-y-2">
                <Label htmlFor="login">Login</Label>
                <Input
                  id="login"
                  type="text"
                  value={loginValue}
                  onChange={(e) => setLoginValue(e.target.value)}
                  placeholder="Enter your login"
                  autoComplete="username"
                  autoFocus
                  required
                />
              </div>
              <div className="space-y-2">
                <Label htmlFor="password">Password</Label>
                <Input
                  id="password"
                  type="password"
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                  placeholder="Enter your password"
                  autoComplete="current-password"
                  required
                />
              </div>
              <Button
                type="submit"
                className="w-full"
                disabled={login.isPending}
              >
                {login.isPending ? 'Signing in...' : 'Sign In'}
              </Button>
            </form>
          </CardContent>
        </Card>
      </motion.div>
    </div>
  );
}
