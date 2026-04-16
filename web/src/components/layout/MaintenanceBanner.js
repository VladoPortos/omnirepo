import { jsx as _jsx, jsxs as _jsxs } from "react/jsx-runtime";
/**
 * Sticky maintenance banner per D-25.
 * 40px height, z-50, amber. Not dismissible.
 * Super-admin sees a "Disable" button.
 */
import { AlertTriangle } from 'lucide-react';
import { useMaintenance } from '@/hooks/useMaintenance';
import { useAuth } from '@/hooks/useAuth';
import { Button } from '@/components/ui/button';
import { api } from '@/api/client';
import { useQueryClient } from '@tanstack/react-query';
export function MaintenanceBanner() {
    const { isMaintenanceMode } = useMaintenance();
    const { isSuperAdmin } = useAuth();
    const qc = useQueryClient();
    if (!isMaintenanceMode)
        return null;
    const handleDisable = async () => {
        await api.post('/admin/maintenance', { enabled: false });
        qc.invalidateQueries({ queryKey: ['maintenance'] });
    };
    return (_jsxs("div", { className: "fixed top-0 left-0 right-0 z-50 flex h-10 items-center justify-center gap-2 bg-amber-600 text-white text-sm font-medium", children: [_jsx(AlertTriangle, { className: "size-4" }), _jsx("span", { children: "Maintenance mode active -- write operations are disabled." }), isSuperAdmin && (_jsx(Button, { variant: "outline", size: "xs", className: "ml-2 border-white/40 text-white hover:bg-white/20 hover:text-white", onClick: handleDisable, children: "Disable" }))] }));
}
