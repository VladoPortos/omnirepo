/**
 * Maintenance mode hook: queries /api/v1/admin/maintenance status.
 */

import { useMaintenance as useMaintenanceQuery } from '@/api/queries';

export function useMaintenance() {
  const { data } = useMaintenanceQuery();
  return { isMaintenanceMode: data?.enabled ?? false };
}
