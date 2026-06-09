/**
 * Unit tests for the GOPROXY case-escaping helper.
 *
 * Rule: every uppercase ASCII letter becomes "!" + its lowercase form;
 * everything else passes through verbatim (slashes, dots, digits,
 * hyphens, already-lowercase letters).
 */

import { describe, expect, it } from 'vitest';
import { escapeGoModulePath } from '../gomod';

describe('escapeGoModulePath', () => {
  it('passes all-lowercase module paths through unchanged', () => {
    expect(escapeGoModulePath('github.com/acme/foo')).toBe(
      'github.com/acme/foo',
    );
  });

  it('escapes a single uppercase letter as !lowercase', () => {
    expect(escapeGoModulePath('github.com/Azure/thing')).toBe(
      'github.com/!azure/thing',
    );
  });

  it('escapes every uppercase letter independently (canonical doc example)', () => {
    expect(escapeGoModulePath('github.com/Azure/Thing')).toBe(
      'github.com/!azure/!thing',
    );
  });

  it('escapes consecutive uppercase letters individually', () => {
    expect(escapeGoModulePath('github.com/ABC/x')).toBe(
      'github.com/!a!b!c/x',
    );
  });

  it('leaves digits, dots, hyphens, and slashes untouched', () => {
    expect(escapeGoModulePath('gopkg.in/yaml.v3')).toBe('gopkg.in/yaml.v3');
    expect(escapeGoModulePath('k8s.io/api-machinery_x2')).toBe(
      'k8s.io/api-machinery_x2',
    );
  });

  it('applies the same rule to version strings (lowercase versions are no-ops)', () => {
    expect(escapeGoModulePath('v1.2.3')).toBe('v1.2.3');
    expect(escapeGoModulePath('v1.2.3-RC1')).toBe('v1.2.3-!r!c1');
  });

  it('handles the empty string', () => {
    expect(escapeGoModulePath('')).toBe('');
  });
});
