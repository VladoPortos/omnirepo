import { describe, expect, it } from 'vitest';
import {
  formatBytes,
  formatDuration,
  formatDurationSeconds,
} from '../format';

describe('formatBytes', () => {
  it('formats byte counts with binary units', () => {
    expect(formatBytes(0)).toBe('0 B');
    expect(formatBytes(512)).toBe('512 B');
    expect(formatBytes(1536)).toBe('1.5 KB');
    expect(formatBytes(1073741824)).toBe('1.0 GB');
  });

  it('clamps negative values to 0 B', () => {
    expect(formatBytes(-5)).toBe('0 B');
  });
});

describe('formatDuration (milliseconds)', () => {
  it('formats sub-minute durations as whole seconds', () => {
    expect(formatDuration(0)).toBe('0s');
    expect(formatDuration(999)).toBe('0s');
    expect(formatDuration(42_000)).toBe('42s');
  });

  it('formats minutes with optional remaining seconds', () => {
    expect(formatDuration(60_000)).toBe('1m');
    expect(formatDuration(95_000)).toBe('1m 35s');
  });

  it('formats hours with optional remaining minutes', () => {
    expect(formatDuration(3_600_000)).toBe('1h');
    expect(formatDuration(3_661_000)).toBe('1h 1m');
  });

  it('clamps negative durations to 0s', () => {
    expect(formatDuration(-1000)).toBe('0s');
  });
});

describe('formatDurationSeconds (seconds)', () => {
  it('returns <1s for sub-second durations', () => {
    expect(formatDurationSeconds(0)).toBe('<1s');
    expect(formatDurationSeconds(0.4)).toBe('<1s');
  });

  it('rounds sub-minute durations to whole seconds', () => {
    expect(formatDurationSeconds(1)).toBe('1s');
    expect(formatDurationSeconds(42.4)).toBe('42s');
    expect(formatDurationSeconds(59.4)).toBe('59s');
  });

  it('formats minutes with zero-padded seconds', () => {
    expect(formatDurationSeconds(60)).toBe('1m 00s');
    expect(formatDurationSeconds(95)).toBe('1m 35s');
    expect(formatDurationSeconds(125.6)).toBe('2m 06s');
  });
});
