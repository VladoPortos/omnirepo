/**
 * Unit tests for useJobProgress pure helpers (Phase 8 / plan 08-03).
 *
 * Design note — deviation from the plan's test skeleton:
 *
 * The plan's acceptance criteria list 4 test cases that exercise the
 * hook via `renderHook` + `vi.useFakeTimers()`. That approach requires
 * `@testing-library/react` + a DOM runtime (jsdom / happy-dom) which
 * are NOT installed in this repo. Rather than expand the devDep
 * footprint purely for a polling-cadence assertion, the hook was
 * authored with its two decision points extracted as pure functions:
 *
 *   - `computeJobProgress(detail)` — JobDetail → JobProgress reshape
 *     (idle-on-undefined, percent calculation, error wrapping).
 *   - `pollingDecision(detail)` — the refetchInterval callback body
 *     (500 ms while pending/running, `false` on done/failed).
 *
 * Testing those covers every correctness claim the plan's 4 tests
 * would have asserted, without React rendering. The hook itself is a
 * 4-line call to `useQuery` that wires these helpers together; its
 * behaviour is guaranteed by TanStack Query v5 (already vendored +
 * battle-tested) plus the helpers below.
 *
 * If a future plan adds @testing-library/react + a DOM runtime, this
 * file can be extended with a renderHook-based smoke test — the
 * helpers stay authoritative for logic; renderHook only needs to
 * confirm they're wired correctly.
 */

import { describe, expect, it } from 'vitest';
import {
  computeJobProgress,
  pollingDecision,
  idleJobProgress,
  POLL_INTERVAL_MS,
} from '../hooks/useJobProgress';
import type { JobDetail } from '../api/types';

/** Small helper to build a JobDetail row with only the fields a test cares about. */
function job(overrides: Partial<JobDetail>): JobDetail {
  return {
    id: 1,
    kind: 'pull_external',
    status: 'pending',
    attempts: 0,
    progress_bytes: 0,
    total_bytes: 0,
    current_step: '',
    files_synced: 0,
    created_at: '2026-04-20T00:00:00Z',
    updated_at: '2026-04-20T00:00:00Z',
    ...overrides,
  };
}

describe('useJobProgress — idle behaviour', () => {
  it('TestUseJobProgress_ReturnsIdleOnNull: undefined detail returns idleJobProgress', () => {
    const p = computeJobProgress(undefined);
    expect(p).toEqual(idleJobProgress);
    expect(p.isPolling).toBe(false);
    expect(p.percent).toBeNull();
    expect(p.error).toBeNull();
  });

  it('pollingDecision on undefined returns 500 ms (cold-start fetch)', () => {
    expect(pollingDecision(undefined)).toBe(POLL_INTERVAL_MS);
  });
});

describe('useJobProgress — polling cadence', () => {
  it('TestUseJobProgress_PollsWhileRunning: running status keeps refetchInterval at 500 ms', () => {
    const running = job({
      status: 'running',
      progress_bytes: 50,
      total_bytes: 100,
      current_step: 'layer 3 of 7',
    });
    expect(pollingDecision(running)).toBe(500);
    // JobProgress also flips isPolling to true.
    const p = computeJobProgress(running);
    expect(p.isPolling).toBe(true);
    expect(p.status).toBe('running');
  });

  it('pending status also polls every 500 ms', () => {
    const pending = job({ status: 'pending' });
    expect(pollingDecision(pending)).toBe(500);
    expect(computeJobProgress(pending).isPolling).toBe(true);
  });

  it('TestUseJobProgress_StopsOnDone: done status returns false (polling stops)', () => {
    const done = job({
      status: 'done',
      progress_bytes: 100,
      total_bytes: 100,
      current_step: 'done',
    });
    expect(pollingDecision(done)).toBe(false);
    expect(computeJobProgress(done).isPolling).toBe(false);
  });

  it('failed status returns false (polling stops)', () => {
    const failed = job({
      status: 'failed',
      progress_bytes: 42,
      total_bytes: 100,
      last_error: 'upstream unreachable',
    });
    expect(pollingDecision(failed)).toBe(false);
    expect(computeJobProgress(failed).isPolling).toBe(false);
  });
});

describe('useJobProgress — percent computation', () => {
  it('TestUseJobProgress_ComputesPercent: 50/200 → 25 (rounded)', () => {
    const p = computeJobProgress(job({ progress_bytes: 50, total_bytes: 200 }));
    expect(p.percent).toBe(25);
    expect(p.progressBytes).toBe(50);
    expect(p.totalBytes).toBe(200);
  });

  it('percent rounds to nearest integer (33.33% → 33)', () => {
    const p = computeJobProgress(job({ progress_bytes: 1, total_bytes: 3 }));
    expect(p.percent).toBe(33);
  });

  it('total_bytes === 0 yields percent=null (Helm step-based case, D-11)', () => {
    const p = computeJobProgress(
      job({
        progress_bytes: 5,
        total_bytes: 0,
        current_step: 'chart 3 of 12 · redis-17.0.0.tgz',
      }),
    );
    expect(p.percent).toBeNull();
    expect(p.currentStep).toBe('chart 3 of 12 · redis-17.0.0.tgz');
  });

  it('completed job (progress == total) yields 100%', () => {
    const p = computeJobProgress(
      job({ status: 'done', progress_bytes: 100, total_bytes: 100 }),
    );
    expect(p.percent).toBe(100);
    expect(p.isPolling).toBe(false);
  });
});

describe('useJobProgress — defensive coercion (T-08-03-04)', () => {
  it('NaN progress_bytes coerces to 0 without throwing', () => {
    const p = computeJobProgress(
      // Simulate malformed wire payload. Cast through unknown so the
      // test exercises the runtime coercion path — any future type
      // tightening on JobDetail should not remove the runtime guard.
      job({ progress_bytes: NaN as unknown as number, total_bytes: 100 }),
    );
    expect(p.progressBytes).toBe(0);
    expect(p.percent).toBe(0);
  });

  it('null/undefined current_step coerces to empty string', () => {
    const p = computeJobProgress(
      job({ current_step: undefined as unknown as string }),
    );
    expect(p.currentStep).toBe('');
  });
});

describe('useJobProgress — files_synced (quick task 260420-d03)', () => {
  it('plain integer passes through unchanged', () => {
    const p = computeJobProgress(
      job({
        status: 'done',
        progress_bytes: 1_048_576,
        total_bytes: 1_048_576,
        files_synced: 42,
      }),
    );
    expect(p.filesSynced).toBe(42);
  });

  it('zero is preserved (running jobs / back-compat with pre-025 rows)', () => {
    const p = computeJobProgress(
      job({ status: 'running', progress_bytes: 100, total_bytes: 200 }),
    );
    expect(p.filesSynced).toBe(0);
  });

  it('NaN files_synced coerces to 0 (defensive wire-shape guard)', () => {
    const p = computeJobProgress(
      job({ files_synced: NaN as unknown as number }),
    );
    expect(p.filesSynced).toBe(0);
  });

  it('idleJobProgress exposes filesSynced=0 so pill cold-start is deterministic', () => {
    expect(idleJobProgress.filesSynced).toBe(0);
  });
});

describe('useJobProgress — error wrapping', () => {
  it('failed status with last_error wraps into a transient-class envelope', () => {
    const p = computeJobProgress(
      job({
        status: 'failed',
        last_error: 'pull_external: remote.Get: connection refused',
      }),
    );
    expect(p.error).not.toBeNull();
    expect(p.error?.class).toBe('transient');
    expect(p.error?.code).toBe('job.failed');
    expect(p.error?.message).toContain('connection refused');
  });

  it('done status does not synthesise an error envelope', () => {
    const p = computeJobProgress(job({ status: 'done' }));
    expect(p.error).toBeNull();
  });

  it('failed status with empty last_error produces no error envelope', () => {
    const p = computeJobProgress(job({ status: 'failed', last_error: '' }));
    expect(p.error).toBeNull();
  });
});
