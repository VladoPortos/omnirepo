import { jsx as _jsx, jsxs as _jsxs } from "react/jsx-runtime";
/**
 * App sidebar per D-01, D-08.
 * Collapsible to 48px (icon-only). Collapse state stored in localStorage.
 * Navigation items with icons. Admin section for super-admins only.
 * User avatar + menu at bottom with theme toggle, profile, sign out.
 */
import { useEffect, useState } from 'react';
import { Link, useLocation } from 'react-router-dom';
import { LayoutDashboard, FolderKanban, Search, Shield, Users, ScrollText, Lock, ScanSearch, Trash2, Archive, Wrench, ChevronDown, Sun, Moon, LogOut, User, } from 'lucide-react';
import { Sidebar as SidebarRoot, SidebarContent, SidebarFooter, SidebarGroup, SidebarGroupContent, SidebarGroupLabel, SidebarHeader, SidebarMenu, SidebarMenuButton, SidebarMenuItem, SidebarMenuSub, SidebarMenuSubButton, SidebarMenuSubItem, SidebarProvider, SidebarRail, } from '@/components/ui/sidebar';
import { DropdownMenu, DropdownMenuTrigger, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, } from '@/components/ui/dropdown-menu';
import { Collapsible, CollapsibleTrigger, CollapsibleContent, } from '@/components/ui/collapsible';
import { Avatar, AvatarFallback } from '@/components/ui/avatar';
import { useAuth } from '@/hooks/useAuth';
import { useTheme } from '@/hooks/useTheme';
const SIDEBAR_STORAGE_KEY = 'omnirepo-sidebar-open';
const mainNavItems = [
    { label: 'Dashboard', icon: LayoutDashboard, path: '/' },
    { label: 'Projects', icon: FolderKanban, path: '/projects' },
    { label: 'Search', icon: Search, path: '/search' },
];
const adminSubItems = [
    { label: 'Users', icon: Users, path: '/admin/users' },
    { label: 'Audit Log', icon: ScrollText, path: '/admin/audit' },
    { label: 'TLS Certificates', icon: Lock, path: '/admin/tls' },
    { label: 'Trivy Database', icon: ScanSearch, path: '/admin/trivy' },
    { label: 'Garbage Collection', icon: Trash2, path: '/admin/gc' },
    { label: 'Trash', icon: Archive, path: '/admin/trash' },
    { label: 'Maintenance', icon: Wrench, path: '/admin/maintenance' },
];
function getInitials(login) {
    return login.slice(0, 2).toUpperCase();
}
export function AppSidebar() {
    const location = useLocation();
    const { user, isSuperAdmin, logout } = useAuth();
    const { theme, toggleTheme } = useTheme();
    const [adminOpen, setAdminOpen] = useState(() => location.pathname.startsWith('/admin'));
    // Read initial sidebar state from localStorage
    const [defaultOpen] = useState(() => {
        if (typeof window === 'undefined')
            return true;
        const stored = localStorage.getItem(SIDEBAR_STORAGE_KEY);
        return stored !== 'false';
    });
    // Persist sidebar state changes
    const handleOpenChange = (open) => {
        localStorage.setItem(SIDEBAR_STORAGE_KEY, String(open));
    };
    // Expand admin section when navigating to admin routes
    useEffect(() => {
        if (location.pathname.startsWith('/admin')) {
            setAdminOpen(true);
        }
    }, [location.pathname]);
    return (_jsx(SidebarProvider, { defaultOpen: defaultOpen, onOpenChange: handleOpenChange, children: _jsxs(SidebarRoot, { collapsible: "icon", className: "border-r border-border", children: [_jsx(SidebarHeader, { children: _jsx(SidebarMenu, { children: _jsx(SidebarMenuItem, { children: _jsxs(SidebarMenuButton, { size: "lg", render: _jsx(Link, { to: "/" }), children: [_jsx("div", { className: "flex aspect-square size-8 items-center justify-center rounded-lg bg-primary text-primary-foreground", children: _jsx("span", { className: "text-sm font-semibold", children: "O" }) }), _jsx("span", { className: "text-lg font-semibold truncate", children: "OmniRepo" })] }) }) }) }), _jsx(SidebarContent, { children: _jsxs(SidebarGroup, { children: [_jsx(SidebarGroupLabel, { children: "Navigation" }), _jsx(SidebarGroupContent, { children: _jsxs(SidebarMenu, { children: [mainNavItems.map((item) => (_jsx(SidebarMenuItem, { children: _jsxs(SidebarMenuButton, { tooltip: item.label, isActive: item.path === '/'
                                                    ? location.pathname === '/'
                                                    : location.pathname.startsWith(item.path), render: _jsx(Link, { to: item.path }), children: [_jsx(item.icon, {}), _jsx("span", { children: item.label })] }) }, item.path))), isSuperAdmin && (_jsx(Collapsible, { open: adminOpen, onOpenChange: setAdminOpen, children: _jsxs(SidebarMenuItem, { children: [_jsxs(CollapsibleTrigger, { render: _jsx(SidebarMenuButton, { tooltip: "Admin", isActive: location.pathname.startsWith('/admin') }), children: [_jsx(Shield, {}), _jsx("span", { children: "Admin" }), _jsx(ChevronDown, { className: "ml-auto transition-transform group-data-[state=open]/collapsible:rotate-180" })] }), _jsx(CollapsibleContent, { children: _jsx(SidebarMenuSub, { children: adminSubItems.map((sub) => (_jsx(SidebarMenuSubItem, { children: _jsxs(SidebarMenuSubButton, { isActive: location.pathname === sub.path, render: _jsx(Link, { to: sub.path }), children: [_jsx(sub.icon, {}), _jsx("span", { children: sub.label })] }) }, sub.path))) }) })] }) }))] }) })] }) }), _jsx(SidebarFooter, { children: _jsx(SidebarMenu, { children: _jsx(SidebarMenuItem, { children: _jsxs(DropdownMenu, { children: [_jsxs(DropdownMenuTrigger, { render: _jsx(SidebarMenuButton, { size: "lg", className: "data-open:bg-sidebar-accent data-open:text-sidebar-accent-foreground" }), children: [_jsx(Avatar, { className: "size-8 rounded-lg", children: _jsx(AvatarFallback, { className: "rounded-lg", children: user ? getInitials(user.login) : '??' }) }), _jsxs("div", { className: "grid flex-1 text-left text-sm leading-tight", children: [_jsx("span", { className: "truncate font-semibold", children: user?.login ?? 'Unknown' }), _jsx("span", { className: "truncate text-xs text-muted-foreground", children: user?.email ?? '' })] }), _jsx(ChevronDown, { className: "ml-auto size-4" })] }), _jsxs(DropdownMenuContent, { className: "w-56", side: "top", align: "end", sideOffset: 4, children: [_jsxs(DropdownMenuItem, { render: _jsx(Link, { to: "/profile" }), children: [_jsx(User, {}), "Profile"] }), _jsxs(DropdownMenuItem, { onClick: toggleTheme, children: [theme === 'dark' ? _jsx(Sun, {}) : _jsx(Moon, {}), theme === 'dark' ? 'Light Mode' : 'Dark Mode'] }), _jsx(DropdownMenuSeparator, {}), _jsxs(DropdownMenuItem, { onClick: () => logout.mutate(), children: [_jsx(LogOut, {}), "Sign Out"] })] })] }) }) }) }), _jsx(SidebarRail, {})] }) }));
}
