import { describe, expect, it } from 'vitest';
import { arrToCsv, csvToArr, setOrUndef } from '../filter-helpers';

describe('csvToArr', () => {
  it('splits comma-separated input into trimmed entries', () => {
    expect(csvToArr('focal, jammy')).toEqual(['focal', 'jammy']);
    expect(csvToArr('  kernel ,podman,  openssh-server  ')).toEqual([
      'kernel',
      'podman',
      'openssh-server',
    ]);
  });

  it('drops empty segments, including trailing commas', () => {
    expect(csvToArr('focal, jammy,')).toEqual(['focal', 'jammy']);
    expect(csvToArr('a,,b, ,c')).toEqual(['a', 'b', 'c']);
  });

  it('returns [] for empty or whitespace-only input', () => {
    expect(csvToArr('')).toEqual([]);
    expect(csvToArr('   ')).toEqual([]);
    expect(csvToArr(',')).toEqual([]);
  });
});

describe('arrToCsv', () => {
  it('joins entries with a comma and space', () => {
    expect(arrToCsv(['focal', 'jammy'])).toBe('focal, jammy');
    expect(arrToCsv(['one'])).toBe('one');
  });

  it('returns empty string for undefined or empty arrays', () => {
    expect(arrToCsv(undefined)).toBe('');
    expect(arrToCsv([])).toBe('');
  });
});

describe('setOrUndef', () => {
  it('returns undefined for an empty array so JSON omits the key', () => {
    expect(setOrUndef([])).toBeUndefined();
  });

  it('passes non-empty arrays through unchanged', () => {
    const arr = ['amd64', 'arm64'];
    expect(setOrUndef(arr)).toBe(arr);
  });
});

describe('round-trip', () => {
  it('csvToArr(arrToCsv(x)) preserves entries', () => {
    const entries = ['kernel', 'podman', 'openssh-server'];
    expect(csvToArr(arrToCsv(entries))).toEqual(entries);
  });
});
