/**
 * Admin Maintenance page.
 * Toggle switch with confirmation dialog for enable, immediate disable.
 */

import { useState, useCallback } from 'react';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { api } from '@/api/client';
import type { MaintenanceStatus, MaintenanceToggle } from '@/api/types';
import { useMaintenance } from '@/api/queries';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
  DialogFooter,
} from '@/components/ui/dialog';
import { Switch } from '@/components/ui/switch';
import { Label } from '@/components/ui/label';
import { toast } from 'sonner';
import { Wrench, AlertTriangle, ShieldCheck } from 'lucide-react';

// ---------- Hooks ----------

function useToggleMaintenance() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (data: MaintenanceToggle) =>
      api.post<MaintenanceStatus>('/admin/maintenance', data),
    onSuccess: (_data, vars) => {
      qc.invalidateQueries({ queryKey: ['maintenance'] });
      toast.success(
        vars.enabled
          ? 'Maintenance mode enabled. Write operations will return 503.'
          : 'Maintenance mode disabled. Normal operations resumed.',
      );
    },
  });
}

// ---------- Component ----------

export default function MaintenancePage() {
  const { data: status, isLoading } = useMaintenance();
  const toggleMutation = useToggleMaintenance();
  const [confirmOpen, setConfirmOpen] = useState(false);

  const isEnabled = status?.enabled ?? false;

  const handleToggle = useCallback(
    (checked: boolean) => {
      if (checked) {
        // Show confirmation before enabling
        setConfirmOpen(true);
      } else {
        // Disable immediately (no confirmation needed)
        toggleMutation.mutate({ enabled: false });
      }
    },
    [toggleMutation],
  );

  const handleConfirmEnable = useCallback(async () => {
    setConfirmOpen(false);
    try {
      await toggleMutation.mutateAsync({ enabled: true });
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to enable maintenance mode');
    }
  }, [toggleMutation]);

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-semibold">Maintenance</h1>
        <p className="text-sm text-muted-foreground">
          Control maintenance mode for this OmniRepo instance.
        </p>
      </div>

      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <Wrench className="size-5" />
            Maintenance Mode
          </CardTitle>
          <CardDescription>
            When enabled, all write operations will return HTTP 503. Read operations continue to work normally.
            A maintenance banner will be shown to all users.
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-6">
          {isLoading ? (
            <div className="h-12 animate-pulse rounded-md bg-muted" />
          ) : (
            <>
              <div className="flex items-center justify-between rounded-lg border p-4">
                <div className="space-y-1">
                  <Label htmlFor="maintenance-switch" className="text-base font-medium">
                    Maintenance Mode
                  </Label>
                  <p className="text-sm text-muted-foreground">
                    {isEnabled
                      ? 'Write operations are currently blocked.'
                      : 'All operations are running normally.'}
                  </p>
                </div>
                <Switch
                  id="maintenance-switch"
                  checked={isEnabled}
                  onCheckedChange={handleToggle}
                  disabled={toggleMutation.isPending}
                />
              </div>

              {/* Status Indicator */}
              <div className="flex items-center gap-3">
                {isEnabled ? (
                  <>
                    <div className="flex size-8 items-center justify-center rounded-full bg-amber-100 dark:bg-amber-900/30">
                      <AlertTriangle className="size-4 text-amber-600 dark:text-amber-400" />
                    </div>
                    <div>
                      <p className="text-sm font-medium text-amber-800 dark:text-amber-300">
                        Maintenance mode is active
                      </p>
                      <p className="text-xs text-muted-foreground">
                        All write operations return 503 Service Unavailable.
                      </p>
                    </div>
                  </>
                ) : (
                  <>
                    <div className="flex size-8 items-center justify-center rounded-full bg-green-100 dark:bg-green-900/30">
                      <ShieldCheck className="size-4 text-green-600 dark:text-green-400" />
                    </div>
                    <div>
                      <p className="text-sm font-medium text-green-800 dark:text-green-300">
                        System operational
                      </p>
                      <p className="text-xs text-muted-foreground">
                        All read and write operations are functioning normally.
                      </p>
                    </div>
                  </>
                )}
              </div>
            </>
          )}
        </CardContent>
      </Card>

      {/* Enable Confirmation Dialog */}
      <Dialog open={confirmOpen} onOpenChange={setConfirmOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle className="flex items-center gap-2">
              <AlertTriangle className="size-5 text-amber-600" />
              Enable Maintenance Mode
            </DialogTitle>
            <DialogDescription>
              All write operations will return 503. Reads will continue to work. Continue?
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setConfirmOpen(false)}>
              Cancel
            </Button>
            <Button onClick={handleConfirmEnable}>
              Enable Maintenance Mode
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
