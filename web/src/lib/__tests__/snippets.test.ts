/**
 * Unit tests for getSnippets per-RepoType shape contracts.
 *
 * Each test asserts:
 *   1. Entry count per RepoType matches the locked counts.
 *   2. Entry labels match the verbatim SnippetList header strings.
 *   3. Key correctness substrings are present in each emitted `cmd` body.
 *
 * docker and rpm are asserted as unchanged from v1.0. Every other RepoType
 * asserts the rewrite shape: APT dual-signing variants with literal
 * `stable main`, PyPI .pypirc block, Helm 4-entry (traditional + OCI), Git
 * Clone + Authenticate (no inline userinfo), RAW -u on both directions with
 * leading comment, S3 with <region> placeholder and leading credential
 * comment.
 */

import { describe, expect, it } from 'vitest';
import { getSnippets } from '../snippets';

const HOST = 'omnirepo.example';
const P = 'acme';

describe('getSnippets', () => {
  it('docker: 3 entries — Login/Pull/Push with 4-segment URL', () => {
    const s = getSnippets('docker', P, 'hub', HOST);
    expect(s).toHaveLength(3);
    expect(s.map((x) => x.label)).toEqual(['Login', 'Pull', 'Push']);
    expect(s[0].cmd).toContain(`docker login ${HOST}`);
    // OCI router requires 4 segments: host/project/docker/repo/image:tag —
    // the 3-segment form (project/repo/image) returns NAME_UNKNOWN.
    expect(s[1].cmd).toContain(
      `docker pull ${HOST}/${P}/docker/hub/<image>:<tag>`,
    );
    expect(s[2].cmd).toContain(
      `docker push ${HOST}/${P}/docker/hub/<image>:<tag>`,
    );
  });

  it('rpm: 1 entry — dnf config (repo_gpgcheck=1, gpgcheck=0)', () => {
    const s = getSnippets('rpm', P, 'stable', HOST);
    expect(s).toHaveLength(1);
    expect(s[0].label).toBe('dnf config');
    // OmniRepo signs repomd.xml but not individual packages, so default dnf
    // `gpgcheck=1` rejects every non-OmniRepo-signed .rpm with
    // "wrong key(s)?". `repo_gpgcheck=1` + `gpgcheck=0` is the correct
    // pass-through-mirror shape.
    expect(s[0].cmd).toContain('repo_gpgcheck=1');
    expect(s[0].cmd).toContain('gpgcheck=0');
    expect(s[0].cmd).not.toMatch(/^gpgcheck=1$/m);
    expect(s[0].cmd).toContain(
      `baseurl=https://${HOST}/${P}/rpm/stable/`,
    );
    expect(s[0].cmd).toContain(
      `gpgkey=https://${HOST}/${P}/rpm/stable/public-key.asc`,
    );
  });

  it('deb: 3 entries — signed-by / legacy / apt source; literal "stable main"; no apt-key add', () => {
    const s = getSnippets('deb', P, 'stable', HOST);
    expect(s).toHaveLength(3);
    expect(s.map((x) => x.label)).toEqual([
      'Signed-by (Debian 12+ / Ubuntu 22.04+)',
      'Legacy signing key (older hosts)',
      'apt source',
    ]);
    // Modern signing-key variant writes to /etc/apt/keyrings
    expect(s[0].cmd).toContain('/etc/apt/keyrings/omnirepo-stable.asc');
    // Legacy variant writes to /etc/apt/trusted.gpg.d
    expect(s[1].cmd).toContain('/etc/apt/trusted.gpg.d/omnirepo-stable.asc');
    // apt source keeps literal `stable main` (not placeholders)
    expect(s[2].cmd).toMatch(/stable main/);
    // signed-by reference appears on the deb line
    expect(s[2].cmd).toContain(
      `[signed-by=/etc/apt/keyrings/omnirepo-stable.asc]`,
    );
    // Deprecated `apt-key add` MUST NOT appear anywhere
    for (const entry of s) {
      expect(entry.cmd).not.toContain('apt-key add');
    }
  });

  it('pypi: 3 entries — pip install / .pypirc / twine upload', () => {
    const s = getSnippets('pypi', P, 'stable', HOST);
    expect(s).toHaveLength(3);
    expect(s.map((x) => x.label)).toEqual([
      'pip install',
      '.pypirc',
      'twine upload',
    ]);
    expect(s[0].cmd).toContain(
      `--index-url https://${HOST}/${P}/pypi/stable/simple/`,
    );
    // .pypirc body shape
    expect(s[1].cmd).toContain('[omnirepo]');
    expect(s[1].cmd).toContain(
      `repository = https://${HOST}/${P}/pypi/stable/legacy/`,
    );
    expect(s[1].cmd).toContain('username = <user>');
    expect(s[1].cmd).toContain('password = <api-key>');
    expect(s[2].cmd).toContain(
      `--repository-url https://${HOST}/${P}/pypi/stable/legacy/`,
    );
  });

  it('helm: 4 entries — 2 traditional + 2 OCI', () => {
    const s = getSnippets('helm', P, 'charts', HOST);
    expect(s).toHaveLength(4);
    expect(s.map((x) => x.label)).toEqual([
      'helm repo add (traditional)',
      'helm pull (traditional)',
      'helm push (OCI)',
      'helm pull (OCI)',
    ]);
    // Traditional repo-add URL
    expect(s[0].cmd).toContain(
      `https://${HOST}/${P}/helm/charts/`,
    );
    // OCI push targets the OCI-form URL
    expect(s[2].cmd).toMatch(/helm push .* oci:\/\//);
    expect(s[2].cmd).toContain(`oci://${HOST}/${P}/helm/charts`);
    // OCI pull also uses the OCI form
    expect(s[3].cmd).toMatch(/helm pull oci:\/\//);
    expect(s[3].cmd).toContain(`oci://${HOST}/${P}/helm/charts`);
  });

  it('git: 2 entries — Clone + Authenticate; no inline userinfo URL', () => {
    const s = getSnippets('git', P, 'repo', HOST);
    expect(s).toHaveLength(2);
    expect(s[0].label).toBe('Clone');
    expect(s[0].cmd).toBe(`git clone https://${HOST}/${P}/git/repo.git`);
    // No inline userinfo URL (credential leakage)
    for (const entry of s) {
      expect(entry.cmd).not.toMatch(/https:\/\/[^/\s]+:[^@\s]+@/);
    }
    expect(s[1].label).toBe('Authenticate');
    // Chosen form is credential.helper store
    expect(s[1].cmd).toContain('credential.helper store');
  });

  it('raw: 2 entries — -u auth on BOTH upload and download with leading comment', () => {
    const s = getSnippets('raw', P, 'blobs', HOST);
    expect(s).toHaveLength(2);
    expect(s.map((x) => x.label)).toEqual(['Upload', 'Download']);
    for (const entry of s) {
      expect(entry.cmd).toContain('-u <user>:<api-key>');
      expect(entry.cmd).toContain(
        '# Use your OmniRepo user + API key; create one at /profile → API Keys',
      );
      expect(entry.cmd).toContain(`https://${HOST}/${P}/raw/blobs/`);
    }
    // Upload is a PUT with -T
    expect(s[0].cmd).toContain('-T <file>');
    // Download is a GET (-O saves the file)
    expect(s[1].cmd).toContain('-O');
  });

  it('s3: 2 entries — <region> placeholder + access-key comment', () => {
    const s = getSnippets('s3', P, 'bucket', HOST);
    expect(s).toHaveLength(2);
    expect(s.map((x) => x.label)).toEqual(['aws configure', 'aws s3 cp']);
    expect(s[0].cmd).toBe('aws configure --profile omnirepo');
    expect(s[1].cmd).toContain(
      '# Access key & secret: create one at /profile → S3 Keys',
    );
    expect(s[1].cmd).toContain('--region <region>');
    expect(s[1].cmd).toContain(`--endpoint-url https://${HOST}/s3`);
  });

  it('returns an empty array for unknown RepoType (defensive default)', () => {
    // @ts-expect-error intentional unknown type to exercise the default branch
    const s = getSnippets('unknown', P, 'r', HOST);
    expect(s).toEqual([]);
  });

  // The scheme parameter propagates through every URL block so operators
  // served the UI over plain HTTP get snippets that point at http://, not an
  // unreachable https://.
  describe('scheme parameter honoured across every RepoType', () => {
    it('rpm baseurl + gpgkey use http when scheme=http', () => {
      const s = getSnippets('rpm', P, 'stable', HOST, 'http');
      expect(s[0].cmd).toContain(`baseurl=http://${HOST}/${P}/rpm/stable/`);
      expect(s[0].cmd).toContain(
        `gpgkey=http://${HOST}/${P}/rpm/stable/public-key.asc`,
      );
      expect(s[0].cmd).not.toContain('https://');
    });

    it('deb modern-key + legacy-key + apt source all respect scheme=http', () => {
      const s = getSnippets('deb', P, 'stable', HOST, 'http');
      for (const entry of s) {
        expect(entry.cmd).not.toContain('https://');
      }
      expect(s[0].cmd).toContain(`http://${HOST}/${P}/deb/stable/`);
      expect(s[2].cmd).toContain(`http://${HOST}/${P}/deb/stable/ stable main`);
    });

    it('pypi index-url + .pypirc + twine all respect scheme=http', () => {
      const s = getSnippets('pypi', P, 'stable', HOST, 'http');
      for (const entry of s) {
        expect(entry.cmd).not.toContain('https://');
      }
      expect(s[0].cmd).toContain(
        `--index-url http://${HOST}/${P}/pypi/stable/simple/`,
      );
      expect(s[1].cmd).toContain(
        `repository = http://${HOST}/${P}/pypi/stable/legacy/`,
      );
    });

    it('helm traditional URL respects scheme=http (OCI helm URLs stay scheme-less)', () => {
      const s = getSnippets('helm', P, 'charts', HOST, 'http');
      expect(s[0].cmd).toContain(`http://${HOST}/${P}/helm/charts/`);
      // OCI entries intentionally do not carry scheme — they use oci://.
      expect(s[2].cmd).toContain(`oci://${HOST}/${P}/helm/charts`);
    });

    it('git clone URL respects scheme=http', () => {
      const s = getSnippets('git', P, 'repo', HOST, 'http');
      expect(s[0].cmd).toBe(`git clone http://${HOST}/${P}/git/repo.git`);
    });

    it('raw upload+download respect scheme=http', () => {
      const s = getSnippets('raw', P, 'blobs', HOST, 'http');
      for (const entry of s) {
        expect(entry.cmd).not.toContain('https://');
        expect(entry.cmd).toContain(`http://${HOST}/${P}/raw/blobs/`);
      }
    });

    it('s3 endpoint respects scheme=http', () => {
      const s = getSnippets('s3', P, 'bucket', HOST, 'http');
      expect(s[1].cmd).toContain(`--endpoint-url http://${HOST}/s3`);
      expect(s[1].cmd).not.toContain('https://');
    });

    it('default scheme stays https (back-compat: no argument == old behaviour)', () => {
      const s = getSnippets('rpm', P, 'stable', HOST);
      expect(s[0].cmd).toContain('https://');
      expect(s[0].cmd).not.toMatch(/http:\/\/(?!.*https)/);
    });
  });
});
