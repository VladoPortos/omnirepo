import { jsx as _jsx, jsxs as _jsxs, Fragment as _Fragment } from "react/jsx-runtime";
/**
 * App root with React Router 7 data router per D-37.
 * AuthGuard and MustChangePasswordGuard for protected routes.
 * Admin pages code-split via React.lazy.
 */
import { lazy, Suspense } from 'react';
import { createBrowserRouter, Navigate } from 'react-router-dom';
import { AppShell } from '@/components/layout/AppShell';
import { LoginPage } from '@/pages/LoginPage';
import { ChangePasswordPage } from '@/pages/ChangePasswordPage';
import { NotFoundPage } from '@/pages/NotFoundPage';
import { DashboardPage } from '@/pages/DashboardPage';
import { ProjectsPage } from '@/pages/ProjectsPage';
import { ProjectDetailPage } from '@/pages/ProjectDetailPage';
import { RepoDetailRouter } from '@/pages/repo/RepoDetailRouter';
import { useAuth } from '@/hooks/useAuth';
import { Skeleton } from '@/components/ui/skeleton';
import { SearchPage } from '@/pages/SearchPage';
import { ProfilePage } from '@/pages/ProfilePage';
// Placeholder for lazy-load error fallbacks
function PlaceholderPage({ name }) {
    return (_jsxs("div", { children: [_jsx("h1", { className: "text-2xl font-semibold", children: name }), _jsx("p", { className: "text-muted-foreground mt-2", children: "Coming in a future plan." })] }));
}
// Lazy-loaded admin pages per D-37 code splitting
const AdminUsersPage = lazy(() => import('@/pages/admin/UsersPage').catch(() => ({
    default: () => _jsx(PlaceholderPage, { name: "Users" }),
})));
const AdminAuditPage = lazy(() => import('@/pages/admin/AuditPage').catch(() => ({
    default: () => _jsx(PlaceholderPage, { name: "Audit Log" }),
})));
const AdminTLSPage = lazy(() => import('@/pages/admin/TLSPage').catch(() => ({
    default: () => _jsx(PlaceholderPage, { name: "TLS Certificates" }),
})));
const AdminTrivyPage = lazy(() => import('@/pages/admin/TrivyPage').catch(() => ({
    default: () => _jsx(PlaceholderPage, { name: "Trivy Database" }),
})));
const AdminGCPage = lazy(() => import('@/pages/admin/GCPage').catch(() => ({
    default: () => _jsx(PlaceholderPage, { name: "Garbage Collection" }),
})));
const AdminTrashPage = lazy(() => import('@/pages/admin/TrashPage').catch(() => ({
    default: () => _jsx(PlaceholderPage, { name: "Trash" }),
})));
const AdminMaintenancePage = lazy(() => import('@/pages/admin/MaintenancePage').catch(() => ({
    default: () => _jsx(PlaceholderPage, { name: "Maintenance" }),
})));
// Loading fallback for lazy routes
function LazyFallback() {
    return (_jsxs("div", { className: "space-y-4 p-4", children: [_jsx(Skeleton, { className: "h-8 w-48" }), _jsx(Skeleton, { className: "h-4 w-full max-w-md" }), _jsx(Skeleton, { className: "h-64 w-full" })] }));
}
// Auth guard: redirects unauthenticated users to /login
function AuthGuard({ children }) {
    const { isAuthenticated, isLoading } = useAuth();
    if (isLoading) {
        return (_jsx("div", { className: "flex min-h-screen items-center justify-center bg-background", children: _jsxs("div", { className: "space-y-4 w-64", children: [_jsx(Skeleton, { className: "h-8 w-48 mx-auto" }), _jsx(Skeleton, { className: "h-4 w-full" })] }) }));
    }
    if (!isAuthenticated) {
        return _jsx(Navigate, { to: "/login", replace: true });
    }
    return _jsx(_Fragment, { children: children });
}
// Must-change-password guard: redirects users who must change password
function MustChangePasswordGuard({ children }) {
    const { mustChangePassword } = useAuth();
    if (mustChangePassword) {
        return _jsx(Navigate, { to: "/change-password", replace: true });
    }
    return _jsx(_Fragment, { children: children });
}
export const router = createBrowserRouter([
    {
        path: '/login',
        element: _jsx(LoginPage, {}),
    },
    {
        path: '/change-password',
        element: _jsx(ChangePasswordPage, {}),
    },
    {
        path: '/',
        element: (_jsx(AuthGuard, { children: _jsx(MustChangePasswordGuard, { children: _jsx(AppShell, {}) }) })),
        children: [
            { index: true, element: _jsx(DashboardPage, {}) },
            { path: 'projects', element: _jsx(ProjectsPage, {}) },
            { path: 'projects/:name', element: _jsx(ProjectDetailPage, {}) },
            { path: 'projects/:name/:type/:repo', element: _jsx(RepoDetailRouter, {}) },
            { path: 'search', element: _jsx(SearchPage, {}) },
            { path: 'profile', element: _jsx(ProfilePage, {}) },
            {
                path: 'admin',
                children: [
                    {
                        path: 'users',
                        element: (_jsx(Suspense, { fallback: _jsx(LazyFallback, {}), children: _jsx(AdminUsersPage, {}) })),
                    },
                    {
                        path: 'audit',
                        element: (_jsx(Suspense, { fallback: _jsx(LazyFallback, {}), children: _jsx(AdminAuditPage, {}) })),
                    },
                    {
                        path: 'tls',
                        element: (_jsx(Suspense, { fallback: _jsx(LazyFallback, {}), children: _jsx(AdminTLSPage, {}) })),
                    },
                    {
                        path: 'trivy',
                        element: (_jsx(Suspense, { fallback: _jsx(LazyFallback, {}), children: _jsx(AdminTrivyPage, {}) })),
                    },
                    {
                        path: 'gc',
                        element: (_jsx(Suspense, { fallback: _jsx(LazyFallback, {}), children: _jsx(AdminGCPage, {}) })),
                    },
                    {
                        path: 'trash',
                        element: (_jsx(Suspense, { fallback: _jsx(LazyFallback, {}), children: _jsx(AdminTrashPage, {}) })),
                    },
                    {
                        path: 'maintenance',
                        element: (_jsx(Suspense, { fallback: _jsx(LazyFallback, {}), children: _jsx(AdminMaintenancePage, {}) })),
                    },
                ],
            },
        ],
    },
    { path: '*', element: _jsx(NotFoundPage, {}) },
]);
