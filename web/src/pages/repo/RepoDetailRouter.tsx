/**
 * Routes to the correct repo detail page based on repo.type.
 * Reads URL params :name (project), :type, :repo and fetches repo detail.
 */

import { Navigate, useParams } from 'react-router-dom';
import { useRepo } from '@/api/queries';
import { NotFoundPage } from '@/pages/NotFoundPage';
import { RepoSkeleton } from './RepoPageLayout';
import { DockerRepoPage } from './DockerRepoPage';
import { RpmRepoPage } from './RpmRepoPage';
import { AptRepoPage } from './AptRepoPage';
import { PypiRepoPage } from './PypiRepoPage';
import { HelmRepoPage } from './HelmRepoPage';
import { RawRepoPage } from './RawRepoPage';
import { GitRepoPage } from './GitRepoPage';
import { GoRepoPage } from './GoRepoPage';
import { NpmRepoPage } from './NpmRepoPage';
import { MavenRepoPage } from './MavenRepoPage';

export function RepoDetailRouter() {
  const { name, type, repo } = useParams<{ name: string; type: string; repo: string }>();
  // useRepo must run unconditionally before any early return — react-hooks/rules-of-hooks.
  // S3 "repos" are buckets in a separate table; the query result is discarded
  // when type === 's3' because the Navigate below redirects immediately.
  const { data, isLoading, isError } = useRepo(name!, type!, repo!);

  if (type === 's3') {
    return <Navigate to={`/projects/${name}/s3/${repo}`} replace />;
  }

  if (isLoading) return <RepoSkeleton />;
  if (isError || !data) return <NotFoundPage />;

  switch (data.type) {
    case 'docker':
      return <DockerRepoPage repo={data} />;
    case 'rpm':
      return <RpmRepoPage repo={data} />;
    case 'deb':
      return <AptRepoPage repo={data} />;
    case 'pypi':
      return <PypiRepoPage repo={data} />;
    case 'helm':
      return <HelmRepoPage repo={data} />;
    case 'go':
      return <GoRepoPage repo={data} />;
    case 'npm':
      return <NpmRepoPage repo={data} />;
    case 'maven':
      return <MavenRepoPage repo={data} />;
    case 'raw':
      return <RawRepoPage repo={data} />;
    case 'git':
      return <GitRepoPage repo={data} />;
    default:
      return <NotFoundPage />;
  }
}
