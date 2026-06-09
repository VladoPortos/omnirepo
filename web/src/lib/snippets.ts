/**
 * Protocol-aware CLI snippet generators.
 *
 * Emits pre-filled commands that the SnippetPanel Sheet and the
 * EmptyState children slot render verbatim (via the shared SnippetList
 * primitive). Each case returns a stable label + multiline `cmd` body.
 *
 * Per-protocol behaviour:
 *
 *   deb:    emit BOTH signing-key variants — modern (signed-by on
 *           /etc/apt/keyrings, Debian 12+ / Ubuntu 22.04+) AND legacy
 *           (/etc/apt/trusted.gpg.d for older hosts). The deprecated
 *           `apt-key` command (removed on Debian 12+) is no longer
 *           emitted. The `deb` line keeps the literal `stable main`
 *           from v1.0 — preserves copy-paste-and-run ergonomics for the
 *           90% single-suite case (deliberately NOT placeholder-ized).
 *   helm:   4 entries — traditional `helm repo add` + `helm pull`,
 *           plus OCI `helm push` + `helm pull`. OCI pushes are
 *           mirrored server-side to the traditional index (v1.1).
 *   s3:     leading comment pointing at /profile → S3 Keys, explicit
 *           `--region <region>` placeholder (no hardcoded us-east-1).
 *   git:    Clone + Authenticate. Authenticate uses
 *           `git config credential.helper store` rather than
 *           `-c http.extraHeader=...`. Both forms work against our
 *           BasicOrAPIKey middleware (verified in
 *           internal/auth/middleware/basic_or_apikey.go) but the
 *           helper form is simpler for users and avoids teaching the
 *           extraHeader mechanic. NO inline userinfo URLs
 *           (`https://user:key@host/…`) anywhere — credential leakage
 *           risk.
 *   raw:    both Upload and Download get `-u <user>:<api-key>` with
 *           a leading `# Use your OmniRepo user + API key; create
 *           one at /profile → API Keys` comment. Symmetric auth
 *           works for both public and private repos without the
 *           user discovering the requirement on a 401.
 *   pypi:   adds a `.pypirc` block alongside `pip install` and
 *           `twine upload`.
 *   docker: unchanged from v1.0 — already correct.
 *   rpm:    unchanged from v1.0 — already correct.
 *   go:     Consume sets GOPROXY at the repo root + GOSUMDB=off (the
 *           public sum DB cannot vouch for privately-hosted modules and
 *           is unreachable in air-gapped networks anyway). Publish is a
 *           curl PUT of a module zip to the GOPROXY upload path; the
 *           module path must be case-escaped per the GOPROXY rule
 *           (uppercase → "!"+lowercase, see lib/gomod.ts).
 *   npm:    Consume points the npm registry at the repo root and adds a
 *           registry-scoped `_auth` line (base64 of user:api-key) —
 *           scoped to the registry URL so credentials never leak to
 *           other registries. Anonymous read works on public repos.
 *           Publish reuses the same auth + `npm publish --registry`.
 *           NO inline userinfo URLs anywhere.
 *   maven:  pom.xml <repositories> block for consume, settings.xml
 *           <server> block for credentials (id must match), and a
 *           <distributionManagement> block + `mvn deploy` for publish
 *           (Gradle's maven-publish plugin works with the same URL +
 *           credentials — mentioned in a comment).
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
  // `scheme` picks http vs https to match the scheme the UI itself was
  // served over. Previously we hard-coded `https://` everywhere —
  // copy-paste-and-run from the empty-state panel failed out of the box
  // when the operator hit OmniRepo over plain HTTP (e.g. the test port
  // :18080, or a reverse proxy that terminates TLS upstream). Default
  // stays 'https' so every call site that has not been migrated yet
  // renders the same snippets as before.
  scheme: 'http' | 'https' = 'https',
): Snippet[] {
  const proto = scheme; // keep parameter name ergonomic; alias for reuse below.
  switch (type) {
    case 'docker':
      // OCI router expects 4 segments —
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
      // The snippet was `gpgcheck=1` — but OmniRepo
      // signs repomd.xml, not individual packages (packages pass through
      // with upstream signatures when mirrored, or are unsigned when
      // uploaded directly). Default dnf with `gpgcheck=1` verifies
      // EACH package signature against the imported repo key and fails
      // ("Import of key(s) didn't help, wrong key(s)?") for everything
      // but self-signed-by-OmniRepo packages. `repo_gpgcheck=1` +
      // `gpgcheck=0` verifies the repomd signature (which OmniRepo DOES
      // sign) and trusts the packages — the correct shape for a
      // pass-through mirror.
      return [
        {
          label: 'dnf config',
          cmd: `[omnirepo-${repo}]\nname=OmniRepo ${repo}\nbaseurl=${proto}://${host}/${project}/rpm/${repo}/\nrepo_gpgcheck=1\ngpgcheck=0\ngpgkey=${proto}://${host}/${project}/rpm/${repo}/public-key.asc`,
        },
      ];
    case 'deb':
      return [
        {
          label: 'Signed-by (Debian 12+ / Ubuntu 22.04+)',
          cmd: `sudo curl -fsSL ${proto}://${host}/${project}/deb/${repo}/public-key.asc -o /etc/apt/keyrings/omnirepo-${repo}.asc\nsudo chmod 0644 /etc/apt/keyrings/omnirepo-${repo}.asc`,
        },
        {
          label: 'Legacy signing key (older hosts)',
          cmd: `sudo curl -fsSL ${proto}://${host}/${project}/deb/${repo}/public-key.asc -o /etc/apt/trusted.gpg.d/omnirepo-${repo}.asc`,
        },
        {
          label: 'apt source',
          cmd: `# Modern (signed-by):\ndeb [signed-by=/etc/apt/keyrings/omnirepo-${repo}.asc] ${proto}://${host}/${project}/deb/${repo}/ stable main\n# Legacy:\ndeb ${proto}://${host}/${project}/deb/${repo}/ stable main`,
        },
      ];
    case 'pypi':
      return [
        {
          label: 'pip install',
          cmd: `pip install --index-url ${proto}://${host}/${project}/pypi/${repo}/simple/ <package>`,
        },
        {
          label: '.pypirc',
          cmd: `[omnirepo]\nrepository = ${proto}://${host}/${project}/pypi/${repo}/legacy/\nusername = <user>\npassword = <api-key>`,
        },
        {
          label: 'twine upload',
          cmd: `twine upload --repository-url ${proto}://${host}/${project}/pypi/${repo}/legacy/ dist/*`,
        },
      ];
    case 'helm':
      return [
        {
          label: 'helm repo add (traditional)',
          cmd: `helm repo add ${repo} ${proto}://${host}/${project}/helm/${repo}/`,
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
    case 'go':
      return [
        {
          label: 'Consume (GOPROXY)',
          cmd: `export GOPROXY=${proto}://${host}/${project}/go/${repo}\nexport GOSUMDB=off\ngo get <module>@<version>`,
        },
        {
          label: 'Publish',
          cmd: `# Use your OmniRepo user + API key; create one at /profile → API Keys\n# module.zip must be a valid Go module zip (e.g. from the go mod download cache).\n# Escape uppercase letters in the module path as "!"+lowercase\n# (e.g. github.com/Azure/Thing → github.com/!azure/!thing).\ncurl -u <user>:<api-key> -T module.zip ${proto}://${host}/${project}/go/${repo}/<escaped-module>/@v/<version>.zip`,
        },
      ];
    case 'npm':
      return [
        {
          label: 'Consume (npm)',
          cmd: `# Point npm at this registry; auth is registry-scoped (skip the _auth\n# line for anonymous read on public repos). Use your OmniRepo user +\n# API key; create one at /profile → API Keys.\nnpm config set registry ${proto}://${host}/${project}/npm/${repo}/\nnpm config set //${host}/${project}/npm/${repo}/:_auth $(echo -n <user>:<api-key> | base64)\nnpm install <package>`,
        },
        {
          label: 'Publish',
          cmd: `# Use your OmniRepo user + API key; create one at /profile → API Keys\nnpm config set //${host}/${project}/npm/${repo}/:_auth $(echo -n <user>:<api-key> | base64)\nnpm publish --registry ${proto}://${host}/${project}/npm/${repo}/`,
        },
      ];
    case 'maven':
      return [
        {
          label: 'pom.xml (consume)',
          cmd: `<repositories>\n  <repository>\n    <id>omnirepo-${repo}</id>\n    <url>${proto}://${host}/${project}/maven/${repo}</url>\n  </repository>\n</repositories>`,
        },
        {
          label: 'settings.xml (credentials)',
          cmd: `<!-- ~/.m2/settings.xml — use your OmniRepo user + API key; create one\n     at /profile → API Keys. The <id> must match the pom.xml entries. -->\n<servers>\n  <server>\n    <id>omnirepo-${repo}</id>\n    <username><user></username>\n    <password><api-key></password>\n  </server>\n</servers>`,
        },
        {
          label: 'Publish (mvn deploy)',
          cmd: `<!-- pom.xml — Gradle users: the maven-publish plugin works with the\n     same URL + credentials. -->\n<distributionManagement>\n  <repository>\n    <id>omnirepo-${repo}</id>\n    <url>${proto}://${host}/${project}/maven/${repo}</url>\n  </repository>\n</distributionManagement>\n\nmvn deploy`,
        },
      ];
    case 'git':
      // Clone + Authenticate. `credential.helper store` chosen over
      // `-c http.extraHeader=...` because it's simpler for users and
      // avoids teaching the extraHeader mechanic; both forms work against
      // the BasicOrAPIKey middleware (username + API-key as password).
      // No inline-userinfo URLs anywhere — prevents credential leakage.
      return [
        {
          label: 'Clone',
          cmd: `git clone ${proto}://${host}/${project}/git/${repo}.git`,
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
          cmd: `# Use your OmniRepo user + API key; create one at /profile → API Keys\ncurl -u <user>:<api-key> -X PUT -T <file> ${proto}://${host}/${project}/raw/${repo}/<path>`,
        },
        {
          label: 'Download',
          cmd: `# Use your OmniRepo user + API key; create one at /profile → API Keys\ncurl -u <user>:<api-key> -O ${proto}://${host}/${project}/raw/${repo}/<path>`,
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
          cmd: `# Access key & secret: create one at /profile → S3 Keys\naws --endpoint-url ${proto}://${host}/s3 --region <region> s3 cp <file> s3://${repo}/<key>`,
        },
      ];
    default:
      return [];
  }
}
