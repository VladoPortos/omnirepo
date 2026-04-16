/**
 * Dialog for API key / S3 key / password reveal per 05-UI-SPEC.
 * Secret cleared from React state on dialog close (T-05-06-02).
 */

import { useState, useCallback, useEffect } from 'react';
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
  DialogFooter,
} from '@/components/ui/dialog';
import { Button } from '@/components/ui/button';
import { CopyButton } from './CopyButton';
import { AlertTriangle } from 'lucide-react';

interface OneTimeRevealProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  secret: string;
  warningText?: string;
}

export function OneTimeReveal({
  open,
  onOpenChange,
  title,
  secret,
  warningText = 'This secret will only be shown once. Copy it now and store it securely.',
}: OneTimeRevealProps) {
  const [internalSecret, setInternalSecret] = useState(secret);

  // Sync secret when it changes (new reveal)
  useEffect(() => {
    if (secret) {
      setInternalSecret(secret);
    }
  }, [secret]);

  const handleClose = useCallback(
    (nextOpen: boolean) => {
      if (!nextOpen) {
        // Clear secret from state on close (T-05-06-02)
        setInternalSecret('');
      }
      onOpenChange(nextOpen);
    },
    [onOpenChange],
  );

  return (
    <Dialog open={open} onOpenChange={handleClose}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>
            <span className="inline-flex items-start gap-2 text-amber-600 dark:text-amber-400">
              <AlertTriangle className="mt-0.5 size-4 shrink-0" />
              {warningText}
            </span>
          </DialogDescription>
        </DialogHeader>

        <div className="relative rounded-md bg-muted p-3 pr-10">
          <code className="block break-all font-mono text-sm leading-relaxed select-all">
            {internalSecret}
          </code>
          <CopyButton
            text={internalSecret}
            className="absolute right-1.5 top-1.5"
          />
        </div>

        <DialogFooter>
          <Button onClick={() => handleClose(false)}>Close</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
