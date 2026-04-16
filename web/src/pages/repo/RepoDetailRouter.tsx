/**
 * Routes to the correct repo detail page based on repo.type.
 * Reads URL params :name (project), :type, :repo and fetches repo detail.
 */

import { useParams } from 'react-router-dom';
import { useRepo } from '@/api/queries';
import { NotFoundPage } from '@/pages/NotFoundPage';
import { RepoSkeleton } from './RepoPageLayout';
import { DockerRepoPage } from './DockerRepoPage';
import { RpmRepoPage } from './RpmRepoPage';
import { AptRepoPage } from './AptRepoPage';
import { PypiRepoPage } from './PypiRepoPage';
import { HelmRepoPage } from './HelmRepoPage';
import { RawRepoPage } from './RawRepoPage';
import { S3BucketPage } from './S3BucketPage';
import { GitRepoPage } from './GitRepoPage';

export function RepoDetailRouter() {
  const { name, type, repo } = useParams<{ name: string; type: string; repo: string }>();
  const { data, isLoading, isError } = useRepo(name!, type!, repo!);

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
    case 'raw':
      return <RawRepoPage repo={data} />;
    case 's3':
      return <S3BucketPage repo={data} />;
    case 'git':
      return <GitRepoPage repo={data} />;
    default:
      return <NotFoundPage />;
  }
}
