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
  // Repo-type segments (lowercase in URL, display-cased in breadcrumbs).
  rpm: 'RPM',
  deb: 'APT',
  pypi: 'PyPI',
  docker: 'Docker',
  helm: 'Helm',
  git: 'Git',
  raw: 'RAW',
  s3: 'S3',
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
          const segmentPath = '/' + segments.slice(0, index + 1).join('/');
          let path = segmentPath;
          const isLast = index === segments.length - 1;
          // `/projects/:name/s3` is not a real route (buckets live at
          // /projects/:name/s3/:bucket); clicking the 's3' crumb should
          // bring the user back to the project page with the S3 tab.
          // `segmentPath` stays the un-rewritten path so the React key
          // remains unique even when two crumbs point at the same href.
          if (
            segment === 's3' &&
            segments[0] === 'projects' &&
            segments.length >= 2 &&
            !isLast
          ) {
            path = `/projects/${segments[1]}`;
          }

          return (
            <span key={segmentPath} className="contents">
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
