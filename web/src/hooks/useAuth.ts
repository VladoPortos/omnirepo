/**
 * Auth hook composing useMe + login/logout/changePassword mutations.
 */

import { useQueryClient } from '@tanstack/react-query';
import { useMe, useLogin, useLogout, useChangePassword } from '@/api/queries';

export function useAuth() {
  const qc = useQueryClient();
  const { data: user, isLoading } = useMe();
  const login = useLogin();
  const logout = useLogout();
  const changePassword = useChangePassword();

  return {
    user: user ?? null,
    isLoading,
    isAuthenticated: !!user,
    mustChangePassword: user?.must_change_password ?? false,
    isSuperAdmin: user?.is_super_admin ?? false,
    login,
    logout,
    changePassword,
    /** Clear all cached queries (used after logout) */
    clearCache: () => qc.clear(),
  };
}

/**
 * useRoleFor returns the caller's role in the named project, or null when:
 * - projectName is undefined/empty
 * - the caller is not authenticated
 * - the caller is not a member of the project (no entry in project_roles)
 *
 * Super-admins bypass membership and act as implicit maintainers everywhere,
 * so this hook returns 'maintainer' for them regardless of project_roles (which
 * is absent/empty on super-admin /me responses per D-16). Callers that need to
 * distinguish "is super-admin" from "is maintainer" should also check
 * isSuperAdmin from useAuth().
 *
 * Usage:
 *   const role = useRoleFor(projectName);
 *   const canWrite = role === 'maintainer';
 */
export function useRoleFor(projectName: string | undefined): 'maintainer' | 'viewer' | null {
  const { data: user } = useMe();
  if (!user || !projectName) return null;
  if (user.is_super_admin) return 'maintainer';
  return (user.project_roles?.[projectName] as 'maintainer' | 'viewer' | undefined) ?? null;
}
