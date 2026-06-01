import { describe, expect, it } from 'vitest';
import { activityTargetHref } from '../activity';

describe('activityTargetHref', () => {
  it('routes project.created to the project page', () => {
    expect(activityTargetHref('project.created', 'acme')).toBe('/projects/acme');
  });

  it('does not link project.deleted events — the target is gone (F-2)', () => {
    expect(activityTargetHref('project.deleted', 'd1-smoke')).toBe('');
  });

  it('does not link project.api-key.create — target_id is a numeric key id (F-1)', () => {
    expect(activityTargetHref('project.api-key.create', '2')).toBe('');
  });

  it('does not link project.api-key.revoke either (F-1)', () => {
    expect(activityTargetHref('project.api-key.revoke', '7')).toBe('');
  });

  it('routes member.added to the parent project page', () => {
    expect(activityTargetHref('member.added', 'acme')).toBe('/projects/acme');
  });

  it('routes repo.* to the repo detail page via the slash-separated target_id', () => {
    expect(activityTargetHref('repo.created', 'acme/rpm/rpms')).toBe(
      '/projects/acme/rpm/rpms',
    );
  });

  it('returns empty for unlinkable event kinds (auth.*, trivy.*, etc.)', () => {
    expect(activityTargetHref('auth.login.success', 'admin')).toBe('');
    expect(activityTargetHref('trivy.db.refreshed', '')).toBe('');
    expect(activityTargetHref('maintenance.toggled', '')).toBe('');
  });

  it('returns empty when target_id is missing', () => {
    expect(activityTargetHref('project.created', '')).toBe('');
  });
});
