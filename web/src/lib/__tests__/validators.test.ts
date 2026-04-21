import { describe, expect, it } from 'vitest';
import { bucketNameSeemsValid } from '../validators';

describe('bucketNameSeemsValid', () => {
  it('rejects empty and whitespace-only input', () => {
    expect(bucketNameSeemsValid('')).toBe(false);
    expect(bucketNameSeemsValid('   ')).toBe(false);
  });

  it('rejects values below the 3-char minimum in the helper copy (F-6)', () => {
    expect(bucketNameSeemsValid('a')).toBe(false);
    expect(bucketNameSeemsValid('ab')).toBe(false);
    expect(bucketNameSeemsValid(' ab ')).toBe(false);
  });

  it('accepts values inside the 3–63 bound', () => {
    expect(bucketNameSeemsValid('abc')).toBe(true);
    expect(bucketNameSeemsValid('my-bucket')).toBe(true);
    expect(bucketNameSeemsValid('a'.repeat(63))).toBe(true);
  });

  it('rejects values above 63 chars', () => {
    expect(bucketNameSeemsValid('a'.repeat(64))).toBe(false);
  });
});
