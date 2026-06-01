import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import tailwindcss from '@tailwindcss/vite';
import path from 'path';

export default defineConfig({
  plugins: [
    react(),
    tailwindcss(),
  ],
  resolve: {
    alias: {
      '@': path.resolve(__dirname, './src'),
    },
  },
  server: {
    port: 5173,
    proxy: {
      '/api': 'http://localhost:8080',
      '/v2': 'http://localhost:8080',
      '/s3': 'http://localhost:8080',
      '/git': 'http://localhost:8080',
    },
  },
  build: {
    outDir: 'dist',
    sourcemap: false,
    // v1.7 / BUNDLE-01..03 — split heavy vendor deps out of the main
    // entry chunk so first-paint downloads less (target: gzipped main
    // entry < 300 KB; v1.6 audit #16 measured ~425 KB pre-split).
    //
    // Strategy is by-package rather than per-module: every Radix
    // primitive lands in `radix`; @dicebear in `dicebear`; lucide-react
    // in `lucide`; markdown rendering deps in `markdown`; the React +
    // React-Router + TanStack Query trio in `react-vendor` (they
    // load synchronously from main and rarely change). Other
    // node_modules dust falls into a single `vendor` catch-all.
    //
    // Shiki is intentionally NOT named here — it already splits via
    // dynamic import() in src/lib/highlight.ts (one chunk per
    // language) and Rollup keeps that working. swagger-ui-dist is NOT
    // in the JS bundle (served as static files from public/swagger/
    // via the Go embed); no chunk needed.
    rollupOptions: {
      output: {
        manualChunks(id) {
          // Function form: matches against the absolute module path
          // Vite resolves at build time. The bare-specifier object
          // form (`{'lucide': ['lucide-react']}`) silently no-ops in
          // Vite 8 + Rollup 4 when the npm package is hoisted under
          // node_modules at a deeper path than the manualChunks key
          // can match — the function form sidesteps that.
          if (!id.includes('node_modules')) return undefined;
          if (
            id.includes('/node_modules/react/') ||
            id.includes('/node_modules/react-dom/') ||
            id.includes('/node_modules/react-router/') ||
            id.includes('/node_modules/react-router-dom/') ||
            id.includes('/node_modules/scheduler/')
          ) {
            return 'react-vendor';
          }
          if (id.includes('/node_modules/@tanstack/')) return 'tanstack';
          if (id.includes('/node_modules/@dicebear/')) return 'dicebear';
          if (id.includes('/node_modules/dompurify/')) return 'sanitize';
          if (id.includes('/node_modules/lucide-react/')) return 'lucide';
          if (id.includes('/node_modules/@radix-ui/')) return 'radix';
          if (
            id.includes('/node_modules/@base-ui/') ||
            id.includes('/node_modules/class-variance-authority/') ||
            id.includes('/node_modules/clsx/') ||
            id.includes('/node_modules/tailwind-merge/')
          ) {
            return 'ui-base';
          }
          // Shiki + Oniguruma stay UNNAMED so Rollup keeps the
          // per-language dynamic-import chunks created by
          // src/lib/highlight.ts (rust, java, python, …). Forcing
          // them into a 'vendor' bucket here collapsed every shiki
          // language into one ~600 KB blob and undid the dynamic-
          // import splitting — never co-bucket dynamically-imported
          // packages here.
          if (
            id.includes('/node_modules/shiki/') ||
            id.includes('/node_modules/@shikijs/') ||
            id.includes('/node_modules/oniguruma-to-es/') ||
            id.includes('/node_modules/regex/')
          ) {
            return undefined;
          }
          // Other long-tail vendor dust. Anything statically imported
          // lands here; dynamic-import chunks remain unaffected
          // because Rollup checks manualChunks AFTER deciding on
          // dynamic boundaries.
          return 'vendor';
        },
      },
    },
  },
});
