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
