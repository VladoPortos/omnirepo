/**
 * Protocol-aware CLI snippet generators per D-16 + Phase 7 S-01..S-09.
 *
 * Emits pre-filled commands that the SnippetPanel Sheet and the EMPTY-03
 * EmptyState children slot render verbatim (via the shared SnippetList
 * primitive). Each case returns a stable label + multiline `cmd` body.
 *
 * Labels are authored in UI-SPEC §SnippetList per-protocol header strings.
 * The Phase 7 rewrites:
 *
 *   S-01 (deb):  emit BOTH signing-key variants — modern (signed-by on
 *                /etc/apt/keyrings, Debian 12+ / Ubuntu 22.04+) AND legacy
 *                (/etc/apt/trusted.gpg.d for older hosts). The
 *                deprecated `apt-key` command (removed on Debian 12+) is
 *                no longer emitted.
 *   S-02 (deb):  `deb` line keeps the literal `stable main` from v1.0 —
 *                preserves copy-paste-and-run ergonomics for the 90%
 *                single-suite case (deliberately NOT placeholder-ized).
 *   S-03 (helm): 4 entries — traditional `helm repo add` + `helm pull`,
 *                plus OCI `helm push` + `helm pull`. OCI pushes are
 *                mirrored server-side to the traditional index (v1.1).
 *   S-04 (s3):   leading comment pointing at /profile → S3 Keys, explicit
 *                `--region <region>` placeholder (no hardcoded us-east-1).
 *   S-05 (git):  Clone + Authenticate. Authenticate uses
 *                `git config credential.helper store` rather than
 *                `-c http.extraHeader=...`. Both forms work against our
 *                BasicOrAPIKey middleware (verified in
 *                internal/auth/middleware/basic_or_apikey.go) but the
 *                helper form is simpler for users and avoids teaching the
 *                extraHeader mechanic. NO inline userinfo URLs
 *                (`https://user:key@host/…`) anywhere — credential
 *                leakage risk per threat T-07-03-01.
 *   S-06 (raw):  both Upload and Download get `-u <user>:<api-key>` with
 *                a leading `# Use your OmniRepo user + API key; create
 *                one at /profile → API Keys` comment. Symmetric auth
 *                works for both public and private repos without the
 *                user discovering the requirement on a 401.
 *   S-07 (pypi): adds a `.pypirc` block alongside `pip install` and
 *                `twine upload` — REQ-02 verbatim shape.
 *   S-08 (docker): unchanged from v1.0 — already correct.
 *   S-09 (rpm):  unchanged from v1.0 — already correct.
 *
 * Placeholders like `<user>`, `<api-key>`, `<region>` render literally —
 * users substitute before running. No real secrets in source.
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
      // F-T11: OCI router expects 4 segments —
      //   /v2/{project}/docker/{repo}/{image}
      // Pushing to the 3-segment shape that this snippet used to render
      // (host/project/repo/image:tag) returns NAME_UNKNOWN because the repo
      // lookup uses segment-3 as the repoName, dropping the {image} part.
      return [
        { label: 'Login', cmd: `docker login ${host}` },
        {
          label: 'Pull',
          cmd: `docker pull ${host}/${project}/docker/${repo}/<image>:<tag>`,
        },
        {
          label: 'Push',
          cmd: `docker push ${host}/${project}/docker/${repo}/<image>:<tag>`,
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
          label: 'Signed-by (Debian 12+ / Ubuntu 22.04+)',
          cmd: `sudo curl -fsSL https://${host}/${project}/deb/${repo}/public-key.asc -o /etc/apt/keyrings/omnirepo-${repo}.asc\nsudo chmod 0644 /etc/apt/keyrings/omnirepo-${repo}.asc`,
        },
        {
          label: 'Legacy signing key (older hosts)',
          cmd: `sudo curl -fsSL https://${host}/${project}/deb/${repo}/public-key.asc -o /etc/apt/trusted.gpg.d/omnirepo-${repo}.asc`,
        },
        {
          label: 'apt source',
          cmd: `# Modern (signed-by):\ndeb [signed-by=/etc/apt/keyrings/omnirepo-${repo}.asc] https://${host}/${project}/deb/${repo}/ stable main\n# Legacy:\ndeb https://${host}/${project}/deb/${repo}/ stable main`,
        },
      ];
    case 'pypi':
      return [
        {
          label: 'pip install',
          cmd: `pip install --index-url https://${host}/${project}/pypi/${repo}/simple/ <package>`,
        },
        {
          label: '.pypirc',
          cmd: `[omnirepo]\nrepository = https://${host}/${project}/pypi/${repo}/legacy/\nusername = <user>\npassword = <api-key>`,
        },
        {
          label: 'twine upload',
          cmd: `twine upload --repository-url https://${host}/${project}/pypi/${repo}/legacy/ dist/*`,
        },
      ];
    case 'helm':
      return [
        {
          label: 'helm repo add (traditional)',
          cmd: `helm repo add ${repo} https://${host}/${project}/helm/${repo}/`,
        },
        {
          label: 'helm pull (traditional)',
          cmd: `helm pull ${repo}/<chart> --version <version>`,
        },
        {
          label: 'helm push (OCI)',
          cmd: `helm push <chart>-<version>.tgz oci://${host}/${project}/helm/${repo}`,
        },
        {
          label: 'helm pull (OCI)',
          cmd: `helm pull oci://${host}/${project}/helm/${repo}/<chart> --version <version>`,
        },
      ];
    case 'git':
      // S-05: Clone + Authenticate. `credential.helper store` chosen over
      // `-c http.extraHeader=...` because it's simpler for users and
      // avoids teaching the extraHeader mechanic; both forms work against
      // the BasicOrAPIKey middleware (username + API-key as password).
      // No inline-userinfo URLs anywhere — prevents credential leakage
      // (threat T-07-03-01).
      return [
        {
          label: 'Clone',
          cmd: `git clone https://${host}/${project}/git/${repo}.git`,
        },
        {
          label: 'Authenticate',
          cmd: `# Store credentials once; git prompts for user + API key on first push/fetch.\ngit config --global credential.helper store\n# Then push/fetch normally; credentials are cached in ~/.git-credentials.`,
        },
      ];
    case 'raw':
      return [
        {
          label: 'Upload',
          cmd: `# Use your OmniRepo user + API key; create one at /profile → API Keys\ncurl -u <user>:<api-key> -X PUT -T <file> https://${host}/${project}/raw/${repo}/<path>`,
        },
        {
          label: 'Download',
          cmd: `# Use your OmniRepo user + API key; create one at /profile → API Keys\ncurl -u <user>:<api-key> -O https://${host}/${project}/raw/${repo}/<path>`,
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
          cmd: `# Access key & secret: create one at /profile → S3 Keys\naws --endpoint-url https://${host}/s3 --region <region> s3 cp <file> s3://${repo}/<key>`,
        },
      ];
    default:
      return [];
  }
}
