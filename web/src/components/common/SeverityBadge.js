import { jsx as _jsx } from "react/jsx-runtime";
/**
 * Badge with severity color per 05-UI-SPEC semantic colors.
 */
import { cn } from '@/lib/utils';
import { Badge } from '@/components/ui/badge';
const severityStyles = {
    critical: 'bg-destructive/10 text-destructive border-destructive/20',
    high: 'bg-orange-500/10 text-orange-600 border-orange-500/20 dark:text-orange-400',
    medium: 'bg-amber-500/10 text-amber-600 border-amber-500/20 dark:text-amber-400',
    low: 'bg-teal-500/10 text-teal-600 border-teal-500/20 dark:text-teal-400',
    unknown: 'bg-muted text-muted-foreground border-border',
};
export function SeverityBadge({ severity, className }) {
    const level = severity.toLowerCase();
    const style = severityStyles[level] ?? severityStyles.unknown;
    return (_jsx(Badge, { variant: "outline", className: cn(style, className), children: severity.charAt(0).toUpperCase() + severity.slice(1).toLowerCase() }));
}
