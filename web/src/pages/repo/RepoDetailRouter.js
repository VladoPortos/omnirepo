import { jsx as _jsx } from "react/jsx-runtime";
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
    const { name, repo } = useParams();
    const { data, isLoading, isError } = useRepo(name, repo);
    if (isLoading)
        return _jsx(RepoSkeleton, {});
    if (isError || !data)
        return _jsx(NotFoundPage, {});
    switch (data.type) {
        case 'docker':
            return _jsx(DockerRepoPage, { repo: data });
        case 'rpm':
            return _jsx(RpmRepoPage, { repo: data });
        case 'deb':
            return _jsx(AptRepoPage, { repo: data });
        case 'pypi':
            return _jsx(PypiRepoPage, { repo: data });
        case 'helm':
            return _jsx(HelmRepoPage, { repo: data });
        case 'raw':
            return _jsx(RawRepoPage, { repo: data });
        case 's3':
            return _jsx(S3BucketPage, { repo: data });
        case 'git':
            return _jsx(GitRepoPage, { repo: data });
        default:
            return _jsx(NotFoundPage, {});
    }
}
