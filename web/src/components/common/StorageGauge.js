import { jsx as _jsx, jsxs as _jsxs } from "react/jsx-runtime";
/**
 * Storage usage visualization using Progress component per D-03.
 * Shows used/total with percentage and accent-colored fill bar.
 */
import { Progress } from '@/components/ui/progress';
import { formatBytes } from '@/lib/format';
import { cn } from '@/lib/utils';
export function StorageGauge({ used, total, className }) {
    const percentage = total > 0 ? Math.min(Math.round((used / total) * 100), 100) : 0;
    return (_jsxs("div", { className: cn('space-y-2', className), children: [_jsxs("div", { className: "flex items-center justify-between text-sm", children: [_jsx("span", { className: "text-muted-foreground", children: "Storage" }), _jsxs("span", { className: "font-medium tabular-nums", children: [formatBytes(used), " / ", formatBytes(total)] })] }), _jsx(Progress, { value: percentage, children: _jsxs("span", { className: "sr-only", children: [percentage, "% used"] }) }), _jsxs("p", { className: "text-xs text-muted-foreground text-right tabular-nums", children: [percentage, "% used"] })] }));
}
