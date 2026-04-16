import { jsx as _jsx, jsxs as _jsxs } from "react/jsx-runtime";
/**
 * Breadcrumbs: renders current route path as breadcrumbs.
 * Uses shadcn Breadcrumb components.
 */
import { Link, useLocation } from 'react-router-dom';
import { Breadcrumb, BreadcrumbList, BreadcrumbItem, BreadcrumbLink, BreadcrumbPage, BreadcrumbSeparator, } from '@/components/ui/breadcrumb';
const LABEL_MAP = {
    '': 'Dashboard',
    projects: 'Projects',
    search: 'Search',
    admin: 'Admin',
    users: 'Users',
    audit: 'Audit Log',
    tls: 'TLS Certificates',
    trivy: 'Trivy Database',
    gc: 'Garbage Collection',
    trash: 'Trash',
    maintenance: 'Maintenance',
    profile: 'Profile',
};
function segmentLabel(segment) {
    return LABEL_MAP[segment] ?? decodeURIComponent(segment);
}
export function Breadcrumbs() {
    const location = useLocation();
    const segments = location.pathname.split('/').filter(Boolean);
    if (segments.length === 0)
        return null;
    return (_jsx(Breadcrumb, { className: "px-8 pt-4 pb-2", children: _jsxs(BreadcrumbList, { children: [_jsx(BreadcrumbItem, { children: _jsx(BreadcrumbLink, { render: _jsx(Link, { to: "/" }), children: "Dashboard" }) }), segments.map((segment, index) => {
                    const path = '/' + segments.slice(0, index + 1).join('/');
                    const isLast = index === segments.length - 1;
                    return (_jsxs("span", { className: "contents", children: [_jsx(BreadcrumbSeparator, {}), _jsx(BreadcrumbItem, { children: isLast ? (_jsx(BreadcrumbPage, { children: segmentLabel(segment) })) : (_jsx(BreadcrumbLink, { render: _jsx(Link, { to: path }), children: segmentLabel(segment) })) })] }, path));
                })] }) }));
}
