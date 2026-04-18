/**
 * Tests for dashboard-thresholds — Phase 7 / plan 07-05.
 *
 * Covers the D-02 threshold defaults and override handling for every
 * pure threshold function. Boundary coverage (just-below / equal /
 * just-above) for each numeric input per the UI-SPEC §Status token
 * → threshold mapping table.
 */

import { describe, expect, it } from 'vitest';
import {
  storageVariant,
  failuresVariant,
  scanFindingsVariant,
  jobsVariant,
  tlsVariant,
  trivyDBVariant,
} from '../dashboard-thresholds';

describe('storageVariant', () => {
  it('total==0 → disabled', () => {
    expect(storageVariant(0, 0)).toBe('disabled');
  });

  it('total<0 → disabled (defensive)', () => {
    expect(storageVariant(10, -5)).toBe('disabled');
  });

  it('ratio 0.5 → healthy', () => {
    expect(storageVariant(50, 100)).toBe('healthy');
  });

  it('ratio 0.69 → healthy (just below warn threshold)', () => {
    expect(storageVariant(69, 100)).toBe('healthy');
  });

  it('ratio 0.70 → warning (equal to warn threshold)', () => {
    expect(storageVariant(70, 100)).toBe('warning');
  });

  it('ratio 0.85 → warning', () => {
    expect(storageVariant(85, 100)).toBe('warning');
  });

  it('ratio 0.90 → warning (equal to fail threshold, still warning)', () => {
    expect(storageVariant(90, 100)).toBe('warning');
  });

  it('ratio 0.91 → failure (just above fail threshold)', () => {
    expect(storageVariant(91, 100)).toBe('failure');
  });

  it('ratio 1.0 → failure', () => {
    expect(storageVariant(100, 100)).toBe('failure');
  });

  it('override: warnRatio=0.5 → ratio 0.55 is warning', () => {
    expect(storageVariant(55, 100, { warnRatio: 0.5 })).toBe('warning');
  });

  it('override: failRatio=0.8 → ratio 0.85 is failure', () => {
    expect(storageVariant(85, 100, { failRatio: 0.8 })).toBe('failure');
  });
});

describe('failuresVariant', () => {
  it('0 → healthy', () => {
    expect(failuresVariant(0)).toBe('healthy');
  });

  it('1 → warning', () => {
    expect(failuresVariant(1)).toBe('warning');
  });

  it('5 → warning (boundary)', () => {
    expect(failuresVariant(5)).toBe('warning');
  });

  it('6 → failure (just above boundary)', () => {
    expect(failuresVariant(6)).toBe('failure');
  });

  it('100 → failure', () => {
    expect(failuresVariant(100)).toBe('failure');
  });

  it('override: warnUpper=2 → 3 is failure', () => {
    expect(failuresVariant(3, { warnUpper: 2 })).toBe('failure');
  });

  it('override: warnUpper=2 → 2 is warning', () => {
    expect(failuresVariant(2, { warnUpper: 2 })).toBe('warning');
  });
});

describe('scanFindingsVariant', () => {
  it('neverScanned=true → disabled (regardless of count)', () => {
    expect(scanFindingsVariant(0, true)).toBe('disabled');
    expect(scanFindingsVariant(99, true)).toBe('disabled');
  });

  it('0 critical, scanned → healthy', () => {
    expect(scanFindingsVariant(0, false)).toBe('healthy');
  });

  it('1 critical → warning', () => {
    expect(scanFindingsVariant(1, false)).toBe('warning');
  });

  it('3 critical → warning', () => {
    expect(scanFindingsVariant(3, false)).toBe('warning');
  });

  it('5 critical → warning (boundary)', () => {
    expect(scanFindingsVariant(5, false)).toBe('warning');
  });

  it('6 critical → failure (just above boundary)', () => {
    expect(scanFindingsVariant(6, false)).toBe('failure');
  });

  it('override: warnUpper=1 → 2 critical is failure', () => {
    expect(scanFindingsVariant(2, false, { warnUpper: 1 })).toBe('failure');
  });
});

describe('jobsVariant', () => {
  // Failure: too many failures (>5)
  it('failedLast24h=6 → failure', () => {
    expect(jobsVariant(0, 0, 6, null)).toBe('failure');
  });

  it('failedLast24h=10 w/ running+queued=0 → failure', () => {
    expect(jobsVariant(0, 0, 10, '2026-04-17T00:00:00Z')).toBe('failure');
  });

  // Warning: some failures and failed > running+queued
  it('failed=3, running=0, queued=0 → warning', () => {
    expect(jobsVariant(0, 0, 3, null)).toBe('warning');
  });

  it('failed=2, running=1, queued=0 (failed > run+queued) → warning', () => {
    expect(jobsVariant(1, 0, 2, null)).toBe('warning');
  });

  it('failed=1, running=0, queued=0 → warning (failed > run+queued)', () => {
    expect(jobsVariant(0, 0, 1, null)).toBe('warning');
  });

  // Healthy: jobs moving — no failures exceed running+queued
  it('running=1, no failures → healthy', () => {
    expect(jobsVariant(1, 0, 0, null)).toBe('healthy');
  });

  it('queued=5, no failures → healthy', () => {
    expect(jobsVariant(0, 5, 0, null)).toBe('healthy');
  });

  it('running=2, queued=1, failed=1 (failed !> run+queued) → healthy', () => {
    expect(jobsVariant(2, 1, 1, null)).toBe('healthy');
  });

  it('running=5, queued=0, failed=5 (failed == run+queued, not >) → healthy', () => {
    expect(jobsVariant(5, 0, 5, null)).toBe('healthy');
  });

  // Healthy: idle and not-problematic — NO 'disabled' or 'maintenance' invented.
  it('all zero + completed timestamp (idle-and-healthy) → healthy', () => {
    expect(jobsVariant(0, 0, 0, '2026-04-17T00:00:00Z')).toBe('healthy');
  });

  it('all zero + null (idle, never run yet) → healthy (NOT disabled)', () => {
    expect(jobsVariant(0, 0, 0, null)).toBe('healthy');
  });
});

describe('tlsVariant', () => {
  it('!hasUploadedCert → disabled (self-signed default)', () => {
    expect(tlsVariant(365, false)).toBe('disabled');
    expect(tlsVariant(0, false)).toBe('disabled');
    expect(tlsVariant(-30, false)).toBe('disabled');
  });

  it('365 days → healthy', () => {
    expect(tlsVariant(365, true)).toBe('healthy');
  });

  it('30 days → healthy (boundary — equal to warn threshold)', () => {
    expect(tlsVariant(30, true)).toBe('healthy');
  });

  it('29 days → warning (just below warn threshold)', () => {
    expect(tlsVariant(29, true)).toBe('warning');
  });

  it('14 days → warning (boundary — equal to fail threshold)', () => {
    expect(tlsVariant(14, true)).toBe('warning');
  });

  it('13 days → failure (just below fail threshold)', () => {
    expect(tlsVariant(13, true)).toBe('failure');
  });

  it('0 days → failure', () => {
    expect(tlsVariant(0, true)).toBe('failure');
  });

  it('negative (expired) → failure', () => {
    expect(tlsVariant(-1, true)).toBe('failure');
  });

  it('override: warnDays=60, failDays=30 → 45 days is warning', () => {
    expect(tlsVariant(45, true, { warnDays: 60, failDays: 30 })).toBe('warning');
  });
});

describe('trivyDBVariant', () => {
  it('!everInitialised → disabled', () => {
    expect(trivyDBVariant(999, false)).toBe('disabled');
    expect(trivyDBVariant(0, false)).toBe('disabled');
  });

  it('0 days → healthy', () => {
    expect(trivyDBVariant(0, true)).toBe('healthy');
  });

  it('7 days → healthy (boundary — equal to warn threshold)', () => {
    expect(trivyDBVariant(7, true)).toBe('healthy');
  });

  it('8 days → warning (just above warn threshold)', () => {
    expect(trivyDBVariant(8, true)).toBe('warning');
  });

  it('14 days → warning', () => {
    expect(trivyDBVariant(14, true)).toBe('warning');
  });

  it('30 days → warning (boundary — equal to fail threshold)', () => {
    expect(trivyDBVariant(30, true)).toBe('warning');
  });

  it('31 days → failure (just above fail threshold)', () => {
    expect(trivyDBVariant(31, true)).toBe('failure');
  });

  it('365 days → failure', () => {
    expect(trivyDBVariant(365, true)).toBe('failure');
  });

  it('override: warnDays=3, failDays=14 → 5 days is warning', () => {
    expect(trivyDBVariant(5, true, { warnDays: 3, failDays: 14 })).toBe('warning');
  });
});
