/**
 * Forced password change page per D-05, TEN-12.
 * Same centered card layout. Banner explaining why.
 */

import { useState, type FormEvent } from 'react';
import { useNavigate } from 'react-router-dom';
import { motion } from 'framer-motion';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { useAuth } from '@/hooks/useAuth';
import { envelopeFromError, localEnvelope, type ApiErrorEnvelope } from '@/api/client';
import { ErrorEnvelopeRenderer } from '@/components/common/ErrorEnvelope';

export function ChangePasswordPage() {
  const navigate = useNavigate();
  const { changePassword } = useAuth();
  const [current, setCurrent] = useState('');
  const [newPassword, setNewPassword] = useState('');
  const [confirmPassword, setConfirmPassword] = useState('');
  const [errorEnvelope, setErrorEnvelope] = useState<ApiErrorEnvelope | null>(null);

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault();
    setErrorEnvelope(null);

    if (newPassword !== confirmPassword) {
      setErrorEnvelope(localEnvelope('Passwords do not match.'));
      return;
    }

    if (newPassword.length < 8) {
      setErrorEnvelope(localEnvelope('Password must be at least 8 characters.'));
      return;
    }

    try {
      await changePassword.mutateAsync({
        current,
        new_password: newPassword,
      });
      navigate('/');
    } catch (err) {
      setErrorEnvelope(
        envelopeFromError(err, 'Unable to reach the server. Check your connection and try again.'),
      );
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
            <CardTitle className="text-2xl">Change Your Password</CardTitle>
          </CardHeader>
          <CardContent>
            <div className="mb-4 rounded-md bg-amber-600/10 border border-amber-600/30 p-3 text-sm text-amber-500">
              Your password must be changed before you can continue.
            </div>
            <form onSubmit={handleSubmit} className="space-y-4">
              {errorEnvelope && (
                <ErrorEnvelopeRenderer envelope={errorEnvelope} />
              )}
              <div className="space-y-2">
                <Label htmlFor="current-password">Current Password</Label>
                <Input
                  id="current-password"
                  type="password"
                  value={current}
                  onChange={(e) => setCurrent(e.target.value)}
                  autoComplete="current-password"
                  required
                />
              </div>
              <div className="space-y-2">
                <Label htmlFor="new-password">New Password</Label>
                <Input
                  id="new-password"
                  type="password"
                  value={newPassword}
                  onChange={(e) => setNewPassword(e.target.value)}
                  autoComplete="new-password"
                  required
                />
              </div>
              <div className="space-y-2">
                <Label htmlFor="confirm-password">Confirm New Password</Label>
                <Input
                  id="confirm-password"
                  type="password"
                  value={confirmPassword}
                  onChange={(e) => setConfirmPassword(e.target.value)}
                  autoComplete="new-password"
                  required
                />
              </div>
              <Button
                type="submit"
                className="w-full"
                disabled={changePassword.isPending}
              >
                {changePassword.isPending ? 'Updating...' : 'Update Password'}
              </Button>
            </form>
          </CardContent>
        </Card>
      </motion.div>
    </div>
  );
}
