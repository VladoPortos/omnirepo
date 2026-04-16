/**
 * App root with React Router 7 data router per D-37.
 * AuthGuard and MustChangePasswordGuard for protected routes.
 * Admin pages code-split via React.lazy.
 */

import { lazy, Suspense, type ReactNode } from 'react';
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
function PlaceholderPage({ name }: { name: string }) {
  return (
    <div>
      <h1 className="text-2xl font-semibold">{name}</h1>
      <p className="text-muted-foreground mt-2">Coming in a future plan.</p>
    </div>
  );
}

// Lazy-loaded admin pages per D-37 code splitting
const AdminUsersPage = lazy(() =>
  import('@/pages/admin/UsersPage').catch(() => ({
    default: () => <PlaceholderPage name="Users" />,
  })),
);
const AdminAuditPage = lazy(() =>
  import('@/pages/admin/AuditPage').catch(() => ({
    default: () => <PlaceholderPage name="Audit Log" />,
  })),
);
const AdminTLSPage = lazy(() =>
  import('@/pages/admin/TLSPage').catch(() => ({
    default: () => <PlaceholderPage name="TLS Certificates" />,
  })),
);
const AdminTrivyPage = lazy(() =>
  import('@/pages/admin/TrivyPage').catch(() => ({
    default: () => <PlaceholderPage name="Trivy Database" />,
  })),
);
const AdminGCPage = lazy(() =>
  import('@/pages/admin/GCPage').catch(() => ({
    default: () => <PlaceholderPage name="Garbage Collection" />,
  })),
);
const AdminTrashPage = lazy(() =>
  import('@/pages/admin/TrashPage').catch(() => ({
    default: () => <PlaceholderPage name="Trash" />,
  })),
);
const AdminMaintenancePage = lazy(() =>
  import('@/pages/admin/MaintenancePage').catch(() => ({
    default: () => <PlaceholderPage name="Maintenance" />,
  })),
);

// Loading fallback for lazy routes
function LazyFallback() {
  return (
    <div className="space-y-4 p-4">
      <Skeleton className="h-8 w-48" />
      <Skeleton className="h-4 w-full max-w-md" />
      <Skeleton className="h-64 w-full" />
    </div>
  );
}

// Auth guard: redirects unauthenticated users to /login
function AuthGuard({ children }: { children: ReactNode }) {
  const { isAuthenticated, isLoading } = useAuth();

  if (isLoading) {
    return (
      <div className="flex min-h-screen items-center justify-center bg-background">
        <div className="space-y-4 w-64">
          <Skeleton className="h-8 w-48 mx-auto" />
          <Skeleton className="h-4 w-full" />
        </div>
      </div>
    );
  }

  if (!isAuthenticated) {
    return <Navigate to="/login" replace />;
  }

  return <>{children}</>;
}

// Must-change-password guard: redirects users who must change password
function MustChangePasswordGuard({ children }: { children: ReactNode }) {
  const { mustChangePassword } = useAuth();

  if (mustChangePassword) {
    return <Navigate to="/change-password" replace />;
  }

  return <>{children}</>;
}

export const router = createBrowserRouter([
  {
    path: '/login',
    element: <LoginPage />,
  },
  {
    path: '/change-password',
    element: <ChangePasswordPage />,
  },
  {
    path: '/',
    element: (
      <AuthGuard>
        <MustChangePasswordGuard>
          <AppShell />
        </MustChangePasswordGuard>
      </AuthGuard>
    ),
    children: [
      { index: true, element: <DashboardPage /> },
      { path: 'projects', element: <ProjectsPage /> },
      { path: 'projects/:name', element: <ProjectDetailPage /> },
      { path: 'projects/:name/:type/:repo', element: <RepoDetailRouter /> },
      { path: 'search', element: <SearchPage /> },
      { path: 'profile', element: <ProfilePage /> },
      {
        path: 'admin',
        children: [
          {
            path: 'users',
            element: (
              <Suspense fallback={<LazyFallback />}>
                <AdminUsersPage />
              </Suspense>
            ),
          },
          {
            path: 'audit',
            element: (
              <Suspense fallback={<LazyFallback />}>
                <AdminAuditPage />
              </Suspense>
            ),
          },
          {
            path: 'tls',
            element: (
              <Suspense fallback={<LazyFallback />}>
                <AdminTLSPage />
              </Suspense>
            ),
          },
          {
            path: 'trivy',
            element: (
              <Suspense fallback={<LazyFallback />}>
                <AdminTrivyPage />
              </Suspense>
            ),
          },
          {
            path: 'gc',
            element: (
              <Suspense fallback={<LazyFallback />}>
                <AdminGCPage />
              </Suspense>
            ),
          },
          {
            path: 'trash',
            element: (
              <Suspense fallback={<LazyFallback />}>
                <AdminTrashPage />
              </Suspense>
            ),
          },
          {
            path: 'maintenance',
            element: (
              <Suspense fallback={<LazyFallback />}>
                <AdminMaintenancePage />
              </Suspense>
            ),
          },
        ],
      },
    ],
  },
  { path: '*', element: <NotFoundPage /> },
]);
