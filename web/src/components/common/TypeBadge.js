import { jsx as _jsx, jsxs as _jsxs } from "react/jsx-runtime";
/**
 * Badge with repo type icon + label. Uses lucide-react icons.
 */
import { Container, Package, Code, Ship, GitBranch, File, Database, } from 'lucide-react';
import { Badge } from '@/components/ui/badge';
import { cn } from '@/lib/utils';
const typeConfig = {
    docker: { icon: Container, label: 'Docker' },
    rpm: { icon: Package, label: 'RPM' },
    deb: { icon: Package, label: 'APT' },
    pypi: { icon: Code, label: 'PyPI' },
    helm: { icon: Ship, label: 'Helm' },
    git: { icon: GitBranch, label: 'Git' },
    raw: { icon: File, label: 'RAW' },
    s3: { icon: Database, label: 'S3' },
};
export function TypeBadge({ type, className }) {
    const config = typeConfig[type];
    if (!config)
        return null;
    const Icon = config.icon;
    return (_jsxs(Badge, { variant: "secondary", className: cn('gap-1', className), children: [_jsx(Icon, { className: "size-3", "data-icon": "inline-start" }), config.label] }));
}
