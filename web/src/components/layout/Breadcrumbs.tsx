/**
 * Breadcrumbs: renders current route path as breadcrumbs.
 * Uses shadcn Breadcrumb components.
 */

import { Link, useLocation } from 'react-router-dom';
import {
  Breadcrumb,
  BreadcrumbList,
  BreadcrumbItem,
  BreadcrumbLink,
  BreadcrumbPage,
  BreadcrumbSeparator,
} from '@/components/ui/breadcrumb';

const LABEL_MAP: Record<string, string> = {
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

function segmentLabel(segment: string): string {
  return LABEL_MAP[segment] ?? decodeURIComponent(segment);
}

export function Breadcrumbs() {
  const location = useLocation();
  const segments = location.pathname.split('/').filter(Boolean);

  if (segments.length === 0) return null;

  return (
    <Breadcrumb className="px-8 pt-4 pb-2">
      <BreadcrumbList>
        <BreadcrumbItem>
          <BreadcrumbLink render={<Link to="/" />}>
            Dashboard
          </BreadcrumbLink>
        </BreadcrumbItem>
        {segments.map((segment, index) => {
          const path = '/' + segments.slice(0, index + 1).join('/');
          const isLast = index === segments.length - 1;

          return (
            <span key={path} className="contents">
              <BreadcrumbSeparator />
              <BreadcrumbItem>
                {isLast ? (
                  <BreadcrumbPage>{segmentLabel(segment)}</BreadcrumbPage>
                ) : (
                  <BreadcrumbLink render={<Link to={path} />}>
                    {segmentLabel(segment)}
                  </BreadcrumbLink>
                )}
              </BreadcrumbItem>
            </span>
          );
        })}
      </BreadcrumbList>
    </Breadcrumb>
  );
}
