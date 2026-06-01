import js from '@eslint/js';
import tseslint from 'typescript-eslint';
import reactHooks from 'eslint-plugin-react-hooks';
import reactRefresh from 'eslint-plugin-react-refresh';

export default tseslint.config(
  { ignores: ['dist', 'public'] },
  js.configs.recommended,
  ...tseslint.configs.recommended,
  {
    files: ['src/**/*.{ts,tsx}'],
    plugins: {
      'react-hooks': reactHooks,
      'react-refresh': reactRefresh,
    },
    rules: {
      ...reactHooks.configs.recommended.rules,
      'react-refresh/only-export-components': ['warn', { allowConstantExport: true }],
      '@typescript-eslint/no-unused-vars': ['error', { argsIgnorePattern: '^_', varsIgnorePattern: '^_' }],
      // The four rules below were added in eslint-plugin-react-hooks v6/v7 as
      // pre-flight checks for the React Compiler migration. They flag valid
      // but compiler-unfriendly patterns (setState in effects, impure calls
      // during render, mutation after render, etc.). Fixing each instance
      // requires a component-level refactor (often "you might not need an
      // effect" rewrites) plus visual UI verification, so we run them as
      // warnings and migrate incrementally rather than gating on a bulk fix.
      // Track-down: enable as 'error' once the existing component findings
      // are migrated through the React Compiler ESLint config.
      'react-hooks/set-state-in-effect': 'warn',
      'react-hooks/set-state-in-render': 'warn',
      'react-hooks/purity': 'warn',
      'react-hooks/immutability': 'warn',
    },
  },
  // shadcn/ui generated components export variant helpers alongside components — not HMR targets
  {
    files: ['src/components/ui/**/*.{ts,tsx}'],
    rules: {
      'react-refresh/only-export-components': 'off',
    },
  },
  // App.tsx is the router definition with guards/helpers, not a single-component HMR module
  {
    files: ['src/App.tsx'],
    rules: {
      'react-refresh/only-export-components': 'off',
    },
  },
);
