/**
 * App sidebar.
 * Collapsible to 48px (icon-only). Collapse state stored in localStorage.
 * Navigation items with icons. Admin section for super-admins only.
 * User avatar + menu at bottom with theme toggle, profile, sign out.
 */

import React, { useState } from 'react';
import { Link, useLocation } from 'react-router-dom';
import {
  LayoutDashboard,
  FolderKanban,
  Search,
  Shield,
  Users,
  ScrollText,
  Lock,
  ScanSearch,
  Trash2,
  Archive,
  Wrench,
  ChevronDown,
  Sun,
  Moon,
  LogOut,
  User,
} from 'lucide-react';
import {
  Sidebar as SidebarRoot,
  SidebarContent,
  SidebarFooter,
  SidebarGroup,
  SidebarGroupContent,
  SidebarGroupLabel,
  SidebarHeader,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarMenuSub,
  SidebarMenuSubButton,
  SidebarMenuSubItem,
  SidebarProvider,
  SidebarRail,
} from '@/components/ui/sidebar';
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
} from '@/components/ui/dropdown-menu';
import {
  Collapsible,
  CollapsibleTrigger,
  CollapsibleContent,
} from '@/components/ui/collapsible';
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

function getInitials(login: string): string {
  return login.slice(0, 2).toUpperCase();
}

export function AppSidebarProvider({ children }: { children: React.ReactNode }) {
  const [defaultOpen] = useState(() => {
    if (typeof window === 'undefined') return true;
    const stored = localStorage.getItem(SIDEBAR_STORAGE_KEY);
    return stored !== 'false';
  });

  const handleOpenChange = (open: boolean) => {
    localStorage.setItem(SIDEBAR_STORAGE_KEY, String(open));
  };

  return (
    <SidebarProvider defaultOpen={defaultOpen} onOpenChange={handleOpenChange}>
      {children}
    </SidebarProvider>
  );
}

export function AppSidebar() {
  const location = useLocation();
  const { user, isSuperAdmin, logout } = useAuth();
  const { theme, toggleTheme } = useTheme();
  const [adminOpen, setAdminOpen] = useState(() =>
    location.pathname.startsWith('/admin'),
  );

  // Expand admin section when navigating to admin routes. Render-phase
  // previous-value guard (React-documented pattern) instead of an effect.
  const [prevPathname, setPrevPathname] = useState(location.pathname);
  if (location.pathname !== prevPathname) {
    setPrevPathname(location.pathname);
    if (location.pathname.startsWith('/admin')) {
      setAdminOpen(true);
    }
  }

  return (
      <SidebarRoot collapsible="icon" className="border-r border-border">
        {/* Header: Logo */}
        <SidebarHeader>
          <SidebarMenu>
            <SidebarMenuItem>
              <SidebarMenuButton size="lg" render={<Link to="/" />}>
                <div className="flex aspect-square size-8 items-center justify-center rounded-lg bg-primary text-primary-foreground">
                  <span className="text-sm font-semibold">O</span>
                </div>
                <span className="text-lg font-semibold truncate">OmniRepo</span>
              </SidebarMenuButton>
            </SidebarMenuItem>
          </SidebarMenu>
        </SidebarHeader>

        {/* Main nav items */}
        <SidebarContent>
          <SidebarGroup>
            <SidebarGroupLabel>Navigation</SidebarGroupLabel>
            <SidebarGroupContent>
              <SidebarMenu>
                {mainNavItems.map((item) => (
                  <SidebarMenuItem key={item.path}>
                    <SidebarMenuButton
                      tooltip={item.label}
                      isActive={
                        item.path === '/'
                          ? location.pathname === '/'
                          : location.pathname.startsWith(item.path)
                      }
                      render={<Link to={item.path} />}
                    >
                      <item.icon />
                      <span>{item.label}</span>
                    </SidebarMenuButton>
                  </SidebarMenuItem>
                ))}

                {/* Admin section - super-admin only */}
                {isSuperAdmin && (
                  <Collapsible
                    open={adminOpen}
                    onOpenChange={setAdminOpen}
                  >
                    <SidebarMenuItem>
                      <CollapsibleTrigger
                        render={
                          <SidebarMenuButton
                            tooltip="Admin"
                            isActive={location.pathname.startsWith('/admin')}
                          />
                        }
                      >
                        <Shield />
                        <span>Admin</span>
                        <ChevronDown className="ml-auto transition-transform group-data-[state=open]/collapsible:rotate-180" />
                      </CollapsibleTrigger>
                      <CollapsibleContent>
                        <SidebarMenuSub>
                          {adminSubItems.map((sub) => (
                            <SidebarMenuSubItem key={sub.path}>
                              <SidebarMenuSubButton
                                isActive={location.pathname === sub.path}
                                render={<Link to={sub.path} />}
                              >
                                <sub.icon />
                                <span>{sub.label}</span>
                              </SidebarMenuSubButton>
                            </SidebarMenuSubItem>
                          ))}
                        </SidebarMenuSub>
                      </CollapsibleContent>
                    </SidebarMenuItem>
                  </Collapsible>
                )}
              </SidebarMenu>
            </SidebarGroupContent>
          </SidebarGroup>
        </SidebarContent>

        {/* Footer: User menu */}
        <SidebarFooter>
          <SidebarMenu>
            <SidebarMenuItem>
              <DropdownMenu>
                <DropdownMenuTrigger
                  render={
                    <SidebarMenuButton
                      size="lg"
                      className="data-open:bg-sidebar-accent data-open:text-sidebar-accent-foreground"
                    />
                  }
                >
                  <Avatar className="size-8 rounded-lg">
                    <AvatarFallback className="rounded-lg">
                      {user ? getInitials(user.login) : '??'}
                    </AvatarFallback>
                  </Avatar>
                  <div className="grid flex-1 text-left text-sm leading-tight">
                    <span className="truncate font-semibold">
                      {user?.login ?? 'Unknown'}
                    </span>
                    <span className="truncate text-xs text-muted-foreground">
                      {user?.email ?? ''}
                    </span>
                  </div>
                  <ChevronDown className="ml-auto size-4" />
                </DropdownMenuTrigger>
                <DropdownMenuContent
                  className="w-56"
                  side="top"
                  align="end"
                  sideOffset={4}
                >
                  <DropdownMenuItem render={<Link to="/profile" />}>
                    <User />
                    Profile
                  </DropdownMenuItem>
                  <DropdownMenuItem onClick={toggleTheme}>
                    {theme === 'dark' ? <Sun /> : <Moon />}
                    {theme === 'dark' ? 'Light Mode' : 'Dark Mode'}
                  </DropdownMenuItem>
                  <DropdownMenuSeparator />
                  <DropdownMenuItem onClick={() => logout.mutate()}>
                    <LogOut />
                    Sign Out
                  </DropdownMenuItem>
                </DropdownMenuContent>
              </DropdownMenu>
            </SidebarMenuItem>
          </SidebarMenu>
        </SidebarFooter>
        <SidebarRail />
      </SidebarRoot>
  );
}
