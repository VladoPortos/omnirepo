---
phase: 05-rest-api-web-ui-production-dockerfile
plan: 02
subsystem: web-frontend
tags: [react, vite, tailwind, shadcn, embed, swagger]
dependency_graph:
  requires: []
  provides: [web-scaffold, spa-embed, swagger-ui-assets]
  affects: [web/*, internal/httpx/spa.go]
tech_stack:
  added: [react@19.1.0, react-dom@19.1.0, vite@6.3.3, tailwindcss@4.1.4, "@tailwindcss/vite@4.1.4", typescript@5.8.3, "shadcn@4.2.0", "@tanstack/react-query@5.74.4", react-router-dom@7.5.2, framer-motion@12.7.4, lucide-react@0.487.0, "@dicebear/core@9.2.2", "@dicebear/collection@9.2.2", swagger-ui-dist@5.21.0]
  patterns: [css-first-tailwind-4, shadcn-components, go-embed-spa, dark-mode-default]
key_files:
  created: [web/package.json, web/vite.config.ts, web/tsconfig.json, web/tsconfig.node.json, web/index.html, web/src/main.tsx, web/src/index.css, web/src/vite-env.d.ts, web/embed.go, web/embed_test.go, web/public/swagger/index.html, web/components.json, web/.gitignore]
  modified: []
decisions:
  - "Used latest available npm versions instead of plan-specified future versions (React 19.1.0 not 19.2.5, Vite 6.3.3 not 8.0.8, Tailwind 4.1.4 not 4.2.2)"
  - "Replaced deprecated toast component with sonner per shadcn v4"
  - "Fixed shadcn Geist font override to use Inter as specified in UI spec"
  - "Deferred @milkdown/kit, shiki, react-diff-viewer-continued to when pages need them"
metrics:
  duration: 5m
  completed: "2026-04-16T09:39:00Z"
  tasks: 2
  files: 85
---

# Phase 05 Plan 02: Frontend Scaffold Summary

React 19 + Vite 6 + Tailwind CSS 4 (CSS-first) + shadcn/ui v4 scaffold with 30 components, self-hosted Inter/JetBrains Mono fonts, dark-mode default, Swagger UI bundled, Go embed directive compiling clean.

## Task Results

| Task | Name | Commit | Key Files |
|------|------|--------|-----------|
| 1 | Vite + React + Tailwind 4 scaffold with all dependencies | 2bd0808 | web/package.json, web/vite.config.ts, web/src/index.css, web/index.html, 30 shadcn components |
| 2 | Go embed file + Swagger UI assets + SPA serving prep | d669941 | web/embed.go, web/embed_test.go, web/public/swagger/index.html |

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Blocking] npm package versions adjusted to latest available**
- **Found during:** Task 1
- **Issue:** Plan specified future versions (React 19.2.5, Vite 8.0.8, Tailwind 4.2.2, TypeScript 6.0.2) that do not exist on npm
- **Fix:** Installed latest available: React 19.1.0, Vite 6.3.3, Tailwind 4.1.4, TypeScript 5.8.3
- **Files modified:** web/package.json

**2. [Rule 3 - Blocking] shadcn toast component deprecated**
- **Found during:** Task 1
- **Issue:** `toast` component deprecated in shadcn v4, CLI refused to install
- **Fix:** Replaced with `sonner` component (shadcn recommended replacement)
- **Files modified:** web/src/components/ui/sonner.tsx

**3. [Rule 1 - Bug] shadcn Geist font override**
- **Found during:** Task 1
- **Issue:** shadcn init inserted `--font-sans: 'Geist Variable'` overriding our Inter font
- **Fix:** Changed to `--font-sans: 'Inter', system-ui, sans-serif` in the @theme inline block
- **Files modified:** web/src/index.css

**4. [Rule 1 - Bug] TypeScript compilation errors**
- **Found during:** Task 1
- **Issue:** tsconfig.node.json missing composite:true, emitDeclarationOnly; missing @types/node for path module; scroll-area.tsx had unused React import
- **Fix:** Added composite/emitDeclarationOnly to tsconfig.node.json, installed @types/node, removed unused import
- **Files modified:** web/tsconfig.node.json, web/src/components/ui/scroll-area.tsx

**5. [Rule 2 - Missing] Deferred heavy dependencies**
- **Found during:** Task 1
- **Issue:** @milkdown/kit, shiki, react-diff-viewer-continued are large dependencies not needed until specific pages are built
- **Fix:** Deferred installation to when markdown editor/code viewer pages are implemented in later plans
- **Files modified:** None

## Verification Results

- `npm run build` succeeds producing dist/index.html and hashed JS/CSS assets
- `web/dist/swagger/index.html` exists with Swagger UI bundle
- `go build ./web/` compiles clean with embedded SPA
- 4 Go embed tests pass (index.html, swagger, fonts, hashed assets)
- 3 self-hosted .woff2 font files present (Inter regular, Inter semibold, JetBrains Mono regular)
- Dark mode default via `class="dark"` on html element
- No tailwind.config.js (CSS-first Tailwind 4 config)
- 30 shadcn/ui components generated in web/src/components/ui/

## Known Stubs

None - this is a scaffold plan. The minimal App component in main.tsx is intentionally placeholder; actual pages are built in subsequent plans (03+).

## Decisions Made

1. **Latest available versions over future versions:** Plan specified unreleased versions; used latest stable from npm instead. Functionally equivalent.
2. **sonner over toast:** shadcn v4 deprecated toast; sonner is the official replacement with identical API surface.
3. **Deferred heavy deps:** @milkdown/kit, shiki, react-diff-viewer-continued deferred to page-building plans to keep the scaffold lean.

## Self-Check: PASSED

- All 11 key files exist on disk
- Both task commits found in git log (2bd0808, d669941)
- 3 .woff2 font files present
- 30 shadcn components generated
- Go embed tests pass (4/4)
