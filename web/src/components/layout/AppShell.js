import { jsx as _jsx, jsxs as _jsxs } from "react/jsx-runtime";
/**
 * App shell layout per D-01: sidebar + content + maintenance banner.
 */
import { Outlet } from 'react-router-dom';
import { AppSidebar } from './Sidebar';
import { Breadcrumbs } from './Breadcrumbs';
import { MaintenanceBanner } from './MaintenanceBanner';
import { useMaintenance } from '@/hooks/useMaintenance';
export function AppShell() {
    const { isMaintenanceMode } = useMaintenance();
    return (_jsxs("div", { className: "flex min-h-screen", children: [_jsx(MaintenanceBanner, {}), _jsx(AppSidebar, {}), _jsxs("main", { className: `flex-1 overflow-auto ${isMaintenanceMode ? 'mt-10' : ''}`, children: [_jsx(Breadcrumbs, {}), _jsx("div", { className: "p-8", children: _jsx(Outlet, {}) })] })] }));
}
