/**
 * Unit tests for getSnippets per-RepoType shape contracts (Phase 7 / 07-03).
 *
 * Each test asserts:
 *   1. Entry count per RepoType matches the S-01..S-09 locked counts.
 *   2. Entry labels match the verbatim UI-SPEC §SnippetList header strings.
 *   3. Key correctness substrings are present in each emitted `cmd` body.
 *
 * S-08 (docker) and S-09 (rpm) are asserted as unchanged from v1.0. Every
 * other RepoType asserts the Phase-7 rewrite shape: APT dual-signing variants
 * with literal `stable main`, PyPI .pypirc block, Helm 4-entry (traditional +
 * OCI), Git Clone + Authenticate (no inline userinfo), RAW -u on both
 * directions with leading comment, S3 with <region> placeholder and leading
 * credential comment.
 */

import { describe, expect, it } from 'vitest';
import { getSnippets } from '../snippets';

const HOST = 'omnirepo.example';
const P = 'acme';

describe('getSnippets', () => {
  it('docker: 3 entries — Login/Pull/Push with 4-segment URL (F-T11)', () => {
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

  it('rpm: 1 entry — dnf config (S-09 unchanged)', () => {
    const s = getSnippets('rpm', P, 'stable', HOST);
    expect(s).toHaveLength(1);
    expect(s[0].label).toBe('dnf config');
    expect(s[0].cmd).toContain('gpgcheck=1');
    expect(s[0].cmd).toContain(
      `baseurl=https://${HOST}/${P}/rpm/stable/`,
    );
  });

  it('deb: 3 entries — signed-by / legacy / apt source; literal "stable main"; no apt-key add (S-01, S-02)', () => {
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
    // apt source keeps literal `stable main` per S-02 (not placeholders)
    expect(s[2].cmd).toMatch(/stable main/);
    // signed-by reference appears on the deb line
    expect(s[2].cmd).toContain(
      `[signed-by=/etc/apt/keyrings/omnirepo-stable.asc]`,
    );
    // Deprecated `apt-key add` MUST NOT appear anywhere (S-01 fix)
    for (const entry of s) {
      expect(entry.cmd).not.toContain('apt-key add');
    }
  });

  it('pypi: 3 entries — pip install / .pypirc / twine upload (S-07)', () => {
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
    // .pypirc body shape per S-07
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

  it('helm: 4 entries — 2 traditional + 2 OCI (S-03)', () => {
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

  it('git: 2 entries — Clone + Authenticate; no inline userinfo URL (S-05)', () => {
    const s = getSnippets('git', P, 'repo', HOST);
    expect(s).toHaveLength(2);
    expect(s[0].label).toBe('Clone');
    expect(s[0].cmd).toBe(`git clone https://${HOST}/${P}/git/repo.git`);
    // No inline userinfo URL (credential leakage per S-05)
    for (const entry of s) {
      expect(entry.cmd).not.toMatch(/https:\/\/[^/\s]+:[^@\s]+@/);
    }
    expect(s[1].label).toBe('Authenticate');
    // Chosen form is credential.helper store
    expect(s[1].cmd).toContain('credential.helper store');
  });

  it('raw: 2 entries — -u auth on BOTH upload and download with leading comment (S-06)', () => {
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

  it('s3: 2 entries — <region> placeholder + access-key comment (S-04)', () => {
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
});
