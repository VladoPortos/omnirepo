/**
 * App root with React Router 7 data router per D-37.
 * AuthGuard and MustChangePasswordGuard for protected routes.
 * Admin pages code-split via React.lazy.
 */

import { lazy, Suspense, type ReactNode } from 'react';
import {
  createBrowserRouter,
  Navigate,
  useLocation,
  type RouteObject,
} from 'react-router-dom';
import { AppShell } from '@/components/layout/AppShell';
import { LoginPage } from '@/pages/LoginPage';
import { SetupPage } from '@/pages/SetupPage';
import { ChangePasswordPage } from '@/pages/ChangePasswordPage';
import { NotFoundPage } from '@/pages/NotFoundPage';
import { DashboardPage } from '@/pages/DashboardPage';
import { ProjectsPage } from '@/pages/ProjectsPage';
import { ProjectDetailPage } from '@/pages/ProjectDetailPage';
import { RepoDetailRouter } from '@/pages/repo/RepoDetailRouter';
import { S3BucketPage } from '@/pages/repo/S3BucketPage';
import { ScanReportPage } from '@/pages/repo/ScanReportPage';
import { useAuth } from '@/hooks/useAuth';
import { useSetupStatus } from '@/api/queries';
import { Skeleton } from '@/components/ui/skeleton';

import { SearchPage } from '@/pages/SearchPage';
import { ProfilePage } from '@/pages/ProfilePage';

// Fallback shown when a code-split chunk fails to load. The usual cause
// is that the OmniRepo server is unreachable (backend stopped, network
// dropped, TLS cert rejected by the browser), so the browser can't fetch
// the lazy JS bundle for this route. The page itself is built and
// deployed — it just couldn't download.
function ChunkLoadFailurePage({ name }: { name: string }) {
  return (
    <div className="max-w-xl">
      <h1 className="text-2xl font-semibold">{name}</h1>
      <p className="text-muted-foreground mt-2">
        Couldn't load this page. The OmniRepo server is likely unreachable
        — the browser failed to fetch the JavaScript bundle for this route.
      </p>
      <ul className="text-muted-foreground mt-3 list-disc pl-5 text-sm space-y-1">
        <li>Check that the OmniRepo server is running.</li>
        <li>
          If you're using a self-signed TLS cert, confirm your browser has
          accepted it (visit the server URL directly and approve the
          warning).
        </li>
        <li>Check the browser devtools Network tab for the failing request.</li>
      </ul>
      <button
        type="button"
        onClick={() => window.location.reload()}
        className="mt-4 inline-flex items-center rounded-md border border-input bg-background px-3 py-1.5 text-sm font-medium hover:bg-accent hover:text-accent-foreground"
      >
        Retry
      </button>
    </div>
  );
}

// Lazy-loaded admin pages per D-37 code splitting
const AdminUsersPage = lazy(() =>
  import('@/pages/admin/UsersPage').catch(() => ({
    default: () => <ChunkLoadFailurePage name="Users" />,
  })),
);
const AdminAuditPage = lazy(() =>
  import('@/pages/admin/AuditPage').catch(() => ({
    default: () => <ChunkLoadFailurePage name="Audit Log" />,
  })),
);
const AdminTLSPage = lazy(() =>
  import('@/pages/admin/TLSPage').catch(() => ({
    default: () => <ChunkLoadFailurePage name="TLS Certificates" />,
  })),
);
const AdminTrivyPage = lazy(() =>
  import('@/pages/admin/TrivyPage').catch(() => ({
    default: () => <ChunkLoadFailurePage name="Trivy Database" />,
  })),
);
const AdminGCPage = lazy(() =>
  import('@/pages/admin/GCPage').catch(() => ({
    default: () => <ChunkLoadFailurePage name="Garbage Collection" />,
  })),
);
const AdminTrashPage = lazy(() =>
  import('@/pages/admin/TrashPage').catch(() => ({
    default: () => <ChunkLoadFailurePage name="Trash" />,
  })),
);
const AdminMaintenancePage = lazy(() =>
  import('@/pages/admin/MaintenancePage').catch(() => ({
    default: () => <ChunkLoadFailurePage name="Maintenance" />,
  })),
);

// Dev-only error-class story page (Phase 6 / plan 03 task 3).
// Lazy-loaded AND conditional on either import.meta.env.DEV (Vite dev
// server) OR import.meta.env.VITE_OMNIREPO_DEV === 'true' (production
// build compiled with the flag — used by the Playwright e2e suite so
// it can drive the story page against a real Go binary + embedded
// SPA).
//
// Both flags are eliminated at build time: when NEITHER is truthy the
// entire import is tree-shaken from the production bundle, which keeps
// T-06-03-04 honest. The Playwright CI build opts in explicitly via
// VITE_OMNIREPO_DEV=true; regular production builds do not.
const DEV_ROUTES_ENABLED =
  import.meta.env.DEV ||
  (import.meta.env.VITE_OMNIREPO_DEV as unknown as string) === 'true';

const ErrorClassStoryPage = DEV_ROUTES_ENABLED
  ? lazy(() =>
      import('@/pages/_dev/ErrorClassStoryPage').then((m) => ({
        default: m.ErrorClassStoryPage,
      })),
    )
  : null;

// Phase 6 / plan 06-06: primitives story page. Same tree-shake gate
// as ErrorClassStoryPage above — dev builds only.
const PrimitivesStoryPage = DEV_ROUTES_ENABLED
  ? lazy(() =>
      import('@/pages/_dev/PrimitivesStoryPage').then((m) => ({
        default: m.PrimitivesStoryPage,
      })),
    )
  : null;

// Phase 6 / plan 06-07: StatusBadge matrix story page. Same
// tree-shake gate — plan 06-08's Playwright suite snapshots the 24
// badge permutations exposed by this page.
const StatusBadgeStoryPage = DEV_ROUTES_ENABLED
  ? lazy(() =>
      import('@/pages/_dev/StatusBadgeStoryPage').then((m) => ({
        default: m.StatusBadgeStoryPage,
      })),
    )
  : null;

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

// Setup guard: when the install has no users yet, funnel every entry point
// to /setup so the operator is never stuck on a login form with no valid
// credentials. Wraps both AuthGuard (protected routes) and /login (otherwise
// users landing on /login directly would see a dead sign-in form).
function SetupGuard({ children }: { children: ReactNode }) {
  const { data, isLoading, isError } = useSetupStatus();
  const location = useLocation();

  if (isLoading) {
    return (
      <div className="flex min-h-screen items-center justify-center bg-background">
        <p className="text-muted-foreground">Checking setup status…</p>
      </div>
    );
  }

  // Fail-open if the status probe errors — the backend might be briefly
  // unreachable, and sending the user to /setup unconditionally would hide
  // the real problem. Real 401s from later calls still work normally.
  if (!isError && data?.needs_setup && location.pathname !== '/setup') {
    return <Navigate to="/setup" replace />;
  }

  return <>{children}</>;
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

// Dev-only routes appended to the top-level router when Vite is in dev
// mode. Conditional at module scope (not inside JSX) so the branch is
// statically eliminated at build time.
const devRoutes: RouteObject[] =
  DEV_ROUTES_ENABLED &&
  ErrorClassStoryPage &&
  PrimitivesStoryPage &&
  StatusBadgeStoryPage
    ? [
        {
          path: '/_dev/error-class-story',
          element: (
            <Suspense fallback={<div className="p-8">Loading…</div>}>
              <ErrorClassStoryPage />
            </Suspense>
          ),
        },
        {
          path: '/_dev/primitives-story',
          element: (
            <Suspense fallback={<div className="p-8">Loading…</div>}>
              <PrimitivesStoryPage />
            </Suspense>
          ),
        },
        {
          path: '/_dev/status-badge-story',
          element: (
            <Suspense fallback={<div className="p-8">Loading…</div>}>
              <StatusBadgeStoryPage />
            </Suspense>
          ),
        },
      ]
    : [];

export const router = createBrowserRouter([
  ...devRoutes,
  {
    path: '/setup',
    element: <SetupPage />,
  },
  {
    path: '/login',
    element: (
      <SetupGuard>
        <LoginPage />
      </SetupGuard>
    ),
  },
  {
    path: '/change-password',
    element: <ChangePasswordPage />,
  },
  {
    path: '/',
    element: (
      <SetupGuard>
        <AuthGuard>
          <MustChangePasswordGuard>
            <AppShell />
          </MustChangePasswordGuard>
        </AuthGuard>
      </SetupGuard>
    ),
    children: [
      { index: true, element: <DashboardPage /> },
      { path: 'projects', element: <ProjectsPage /> },
      { path: 'projects/:name', element: <ProjectDetailPage /> },
      // Static-segment s3 bucket route must precede the generic
      // /:type/:repo pattern — React Router 7 picks the more specific
      // match, but ordering keeps intent obvious.
      { path: 'projects/:name/s3/:bucket', element: <S3BucketPage /> },
      {
        path: 'projects/:name/:type/:repo/scans/:id',
        element: <ScanReportPage />,
      },
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
      // F-T12: nested catch-all keeps NotFoundPage inside AppShell (sidebar,
      // breadcrumbs) for /projects/:name/:type and similar wrong-but-still-
      // authenticated paths. The top-level `path: '*'` below still renders
      // NotFoundPage chrome-less for pre-auth routes (e.g. /bogus on a fresh
      // install) — no AppShell is available before AuthGuard anyway.
      { path: '*', element: <NotFoundPage /> },
    ],
  },
  { path: '*', element: <NotFoundPage /> },
]);
