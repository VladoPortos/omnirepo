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
    // Match both colocated `foo.test.ts` and nested `__tests__/foo.test.ts`
    // layouts. Phase 7 / 07-03 shipped the nested convention for
    // `src/lib/__tests__/` (snippets, dashboard-thresholds). Phase 8 /
    // 08-03 adds the colocated pattern for hooks (useJobProgress.test.ts
    // next to useJobProgress.ts) because the hook lives under
    // `src/hooks/` which has no existing `__tests__/` dir.
    include: ['src/**/__tests__/**/*.test.ts', 'src/**/*.test.ts'],
  },
  resolve: {
    alias: {
      '@': path.resolve(__dirname, 'src'),
    },
  },
});
