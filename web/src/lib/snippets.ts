/**
 * Protocol-aware CLI snippet generators per D-16.
 * Each repo type produces pre-filled commands for the SnippetPanel.
 */

import type { RepoType } from '@/api/types';

export interface Snippet {
  label: string;
  cmd: string;
}

export function getSnippets(
  type: RepoType,
  project: string,
  repo: string,
  host: string,
): Snippet[] {
  switch (type) {
    case 'docker':
      return [
        { label: 'Login', cmd: `docker login ${host}` },
        {
          label: 'Pull',
          cmd: `docker pull ${host}/${project}/${repo}/<image>:<tag>`,
        },
        {
          label: 'Push',
          cmd: `docker push ${host}/${project}/${repo}/<image>:<tag>`,
        },
      ];
    case 'rpm':
      return [
        {
          label: 'dnf config',
          cmd: `[omnirepo-${repo}]\nname=OmniRepo ${repo}\nbaseurl=https://${host}/${project}/rpm/${repo}/\ngpgcheck=1\ngpgkey=https://${host}/${project}/rpm/${repo}/public-key.asc`,
        },
      ];
    case 'deb':
      return [
        {
          label: 'apt source',
          cmd: `deb https://${host}/${project}/deb/${repo}/ stable main`,
        },
        {
          label: 'import key',
          cmd: `curl -fsSL https://${host}/${project}/deb/${repo}/public-key.asc | sudo apt-key add -`,
        },
      ];
    case 'pypi':
      return [
        {
          label: 'pip install',
          cmd: `pip install --index-url https://${host}/${project}/pypi/${repo}/simple/ <package>`,
        },
        {
          label: 'twine upload',
          cmd: `twine upload --repository-url https://${host}/${project}/pypi/${repo}/legacy/ dist/*`,
        },
      ];
    case 'helm':
      return [
        {
          label: 'helm repo add',
          cmd: `helm repo add ${repo} https://${host}/${project}/helm/${repo}/`,
        },
        { label: 'helm pull', cmd: `helm pull ${repo}/<chart>` },
      ];
    case 'git':
      return [
        {
          label: 'git clone',
          cmd: `git clone https://${host}/git/${project}/${repo}.git`,
        },
      ];
    case 'raw':
      return [
        {
          label: 'Upload',
          cmd: `curl -X PUT -T <file> https://${host}/${project}/raw/${repo}/<path>`,
        },
        {
          label: 'Download',
          cmd: `curl -O https://${host}/${project}/raw/${repo}/<path>`,
        },
      ];
    case 's3':
      return [
        {
          label: 'aws configure',
          cmd: `aws configure --profile omnirepo`,
        },
        {
          label: 'aws s3 cp',
          cmd: `aws --endpoint-url https://${host}/s3 s3 cp <file> s3://${repo}/<key>`,
        },
      ];
  }
}
