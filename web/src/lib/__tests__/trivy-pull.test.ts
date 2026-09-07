import { expect, it } from 'vitest';
import { completedPull, supersededPull } from '../trivy-pull';

it('recognizes fast success and failure without observing running', () => {
  for (const state of ['success', 'failure'] as const) {
    expect(completedPull({state, bytes_downloaded: 0, started_at: 'new'}, 'success', {previousStartedAt:'old'})).toBe(true);
  }
});
it('does not confuse a cached terminal result with the requested pull', () => {
  expect(completedPull({state:'success', bytes_downloaded:0, started_at:'old'}, 'success', {previousStartedAt:'old'})).toBe(false);
});
it('recognizes a known request and resumed running job', () => {
  expect(completedPull({state:'success', bytes_downloaded:0, started_at:'new'}, undefined, {expectedStartedAt:'new'})).toBe(true);
  expect(completedPull({state:'failure', bytes_downloaded:0, started_at:'old'}, 'running', null)).toBe(true);
});
it('does not announce historic terminal states on page mount', () => {
  expect(completedPull({state:'success', bytes_downloaded:0, started_at:'old'}, undefined, null)).toBe(false);
});

it('stops waiting when a newer pull has replaced the requested terminal result', () => {
  for (const state of ['success', 'failure'] as const) {
    expect(completedPull({ state, bytes_downloaded: 0, started_at: '2026-09-07T19:00:00.123456790Z' }, 'success', {
      expectedStartedAt: '2026-09-07T19:00:00.123456789Z',
    })).toBe(true);
  }
});

it('keeps waiting when an older terminal result shares the expected millisecond', () => {
  expect(completedPull({ state: 'success', bytes_downloaded: 0, started_at: '2026-09-07T19:00:00.123456789Z' }, 'success', {
    expectedStartedAt: '2026-09-07T19:00:00.123456790Z',
  })).toBe(false);
});

it('orders whole seconds and variable-length fractions correctly', () => {
  expect(completedPull({ state: 'success', bytes_downloaded: 0, started_at: '2026-09-07T19:00:00.1Z' }, 'success', {
    expectedStartedAt: '2026-09-07T19:00:00Z',
  })).toBe(true);
  expect(completedPull({ state: 'success', bytes_downloaded: 0, started_at: '2026-09-07T19:00:00.12Z' }, 'success', {
    expectedStartedAt: '2026-09-07T19:00:00.123Z',
  })).toBe(false);
});

it('distinguishes supersession from the requested outcome for truthful notifications', () => {
  const expectedStartedAt = '2026-09-07T19:00:00.123456789Z';
  expect(supersededPull({ state: 'failure', bytes_downloaded: 0, started_at: '2026-09-07T19:00:00.123456790Z' }, { expectedStartedAt })).toBe(true);
  expect(supersededPull({ state: 'success', bytes_downloaded: 0, started_at: expectedStartedAt }, { expectedStartedAt })).toBe(false);
});
