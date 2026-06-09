/**
 * Breadcrumbs: renders current route path as breadcrumbs.
 * Uses shadcn Breadcrumb components.
 */

import { Link, useLocation, useMatches } from 'react-router-dom';
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
  scans: 'Scans',
  // Repo-type segments (lowercase in URL, display-cased in breadcrumbs).
  rpm: 'RPM',
  deb: 'APT',
  pypi: 'PyPI',
  docker: 'Docker',
  helm: 'Helm',
  go: 'Go',
  npm: 'npm',
  maven: 'Maven',
  git: 'Git',
  raw: 'RAW',
  s3: 'S3',
};

function segmentLabel(segment: string, context?: { parent?: string }): string {
  // Numeric scan ids get a "#42" prefix so the crumb reads as an id
  // rather than a count or coincidental segment value.
  if (context?.parent === 'scans' && /^\d+$/.test(segment)) {
    return `#${segment}`;
  }
  return LABEL_MAP[segment] ?? decodeURIComponent(segment);
}

export function Breadcrumbs() {
  const location = useLocation();
  const matches = useMatches();
  const segments = location.pathname.split('/').filter(Boolean);

  if (segments.length === 0) return null;

  // F-11: when the current route is our nested catch-all NotFoundPage,
  // render every segment as a non-link BreadcrumbPage. Keeping the
  // breadcrumbs visible preserves the chrome (sidebar, path context) but
  // we stop advertising clickable hrefs that would 404 themselves. The
  // AppShell-scoped catch-all route is tagged `id: 'not-found'` in
  // App.tsx.
  const isNotFound = matches.some((m) => m.id === 'not-found');

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

          // `/projects/:name/:type` is not a real route — repo types don't
          // have standalone pages. Rewrite the type crumb's target to the
          // project detail page so users can navigate "one step up" with a
          // single click. The separator+crumb still reads naturally
          // (Projects > e2e > RAW > raw-scan-on) even if RAW points at the
          // same destination as "e2e" — it's the nearest meaningful parent.
          const isRepoTypeCrumb =
            segments[0] === 'projects' &&
            index === 2 &&
            segments.length >= 4 &&
            segment in LABEL_MAP;
          if (isRepoTypeCrumb) {
            path = `/projects/${segments[1]}`;
          }

          // `.../scans` is a REST prefix under a repo, not a landing page;
          // rendering it as a link would 404. Make it a non-link label so
          // the crumb chain still reads correctly for scan report URLs.
          const isScansPrefixCrumb =
            segments[0] === 'projects' &&
            segment === 'scans' &&
            index === 4 &&
            segments.length === 6;

          const nonLink = isLast || isScansPrefixCrumb || isNotFound;
          return (
            <span key={segmentPath} className="contents">
              <BreadcrumbSeparator />
              <BreadcrumbItem>
                {nonLink ? (
                  <BreadcrumbPage>
                    {segmentLabel(segment, { parent: segments[index - 1] })}
                  </BreadcrumbPage>
                ) : (
                  <BreadcrumbLink render={<Link to={path} />}>
                    {segmentLabel(segment, { parent: segments[index - 1] })}
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
