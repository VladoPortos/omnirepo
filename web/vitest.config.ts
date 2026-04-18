/**
 * Vitest config for unit tests (Phase 7 / plan 07-03).
 *
 * Scope: unit-test pure TS modules under web/src/ — first consumer is
 * web/src/lib/__tests__/snippets.test.ts. Node environment is sufficient
 * because snippets.ts is dependency-free (no React, no DOM).
 *
 * Alias matches the `@/` import used by the rest of the codebase.
 */

import path from 'node:path';
import { defineConfig } from 'vitest/config';

export default defineConfig({
  test: {
    environment: 'node',
    include: ['src/**/__tests__/**/*.test.ts'],
  },
  resolve: {
    alias: {
      '@': path.resolve(__dirname, 'src'),
    },
  },
});
