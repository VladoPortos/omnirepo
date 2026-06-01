/**
 * App shell layout: sidebar + content + maintenance banner.
 */

import { Outlet } from 'react-router-dom';
import { AppSidebar, AppSidebarProvider } from './Sidebar';
import { Breadcrumbs } from './Breadcrumbs';
import { MaintenanceBanner } from './MaintenanceBanner';
import { useMaintenance } from '@/hooks/useMaintenance';

export function AppShell() {
  const { isMaintenanceMode } = useMaintenance();

  return (
    <AppSidebarProvider>
      <MaintenanceBanner />
      <AppSidebar />
      <main
        className={`flex-1 overflow-auto ${isMaintenanceMode ? 'mt-10' : ''}`}
      >
        <Breadcrumbs />
        <div className="p-8">
          <Outlet />
        </div>
      </main>
    </AppSidebarProvider>
  );
}
