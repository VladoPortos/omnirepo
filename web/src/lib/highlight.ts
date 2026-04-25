/**
 * Shiki syntax highlighter with on-demand language loading per D-38.
 * Uses shiki/core to avoid bundling all grammars upfront.
 *
 * FRONTFIX-01 (Phase 07): the previous implementation called
 *   await import(`@shikijs/langs/${lang}`)
 * with a template-literal specifier. Vite's production build emits a
 * "Missing './${lang}' specifier" warning for that pattern (Rollup's
 * dynamic-import analysis can't enumerate the legal target set when the
 * specifier contains an interpolation that flows into a sub-path of a
 * package export map), and at runtime the lookup against the bundled
 * chunk graph fails with `TypeError: Failed to fetch dynamically
 * imported module` for every language. Highlighting silently fell back
 * to plain text in production.
 *
 * Replaced with an explicit static-import map keyed by Shiki language
 * id. Each entry is a code-split chunk (Vite handles the splitting
 * because the `import()` call sites are statically analysable), so the
 * upfront cost is still just `shiki/core` + the engine WASM; grammars
 * load on first use exactly as before — but the build is warning-free
 * and runtime resolution is deterministic.
 *
 * Adding a new language: import its grammar in LANG_LOADERS below and
 * add the file-extension entry to EXT_MAP. There is no auto-discovery
 * by design — the static map is the authoritative list of what we
 * highlight.
 */

import { createHighlighterCore } from 'shiki/core';
import { createOnigurumaEngine } from 'shiki/engine/oniguruma';

let highlighterPromise: ReturnType<typeof createHighlighterCore> | null = null;

function getHighlighter() {
  if (!highlighterPromise) {
    highlighterPromise = createHighlighterCore({
      themes: [
        import('@shikijs/themes/github-dark'),
        import('@shikijs/themes/github-light'),
      ],
      langs: [],
      engine: createOnigurumaEngine(import('shiki/wasm')),
    });
  }
  return highlighterPromise;
}

export type HighlightTheme = 'github-dark' | 'github-light';

/**
 * LANG_LOADERS maps a Shiki language id to a thunk that lazily imports
 * the grammar module. Vite statically analyses each `import()` call
 * site and emits a separate chunk per language; the thunks are not
 * invoked until highlightCode resolves a non-`text` lang.
 *
 * The set is deliberately small — these are the grammars we ship for
 * the Git file viewer. Anything not in this map (and not `text`) falls
 * back to plain text rendering, which is the same behaviour as a
 * grammar-load failure.
 */
const LANG_LOADERS: Record<string, () => Promise<unknown>> = {
  go: () => import('@shikijs/langs/go'),
  typescript: () => import('@shikijs/langs/typescript'),
  tsx: () => import('@shikijs/langs/tsx'),
  javascript: () => import('@shikijs/langs/javascript'),
  jsx: () => import('@shikijs/langs/jsx'),
  python: () => import('@shikijs/langs/python'),
  ruby: () => import('@shikijs/langs/ruby'),
  rust: () => import('@shikijs/langs/rust'),
  java: () => import('@shikijs/langs/java'),
  c: () => import('@shikijs/langs/c'),
  cpp: () => import('@shikijs/langs/cpp'),
  yaml: () => import('@shikijs/langs/yaml'),
  json: () => import('@shikijs/langs/json'),
  markdown: () => import('@shikijs/langs/markdown'),
};

/**
 * Languages we expose via detectLanguage but do NOT have a grammar
 * loader for. Surfacing them as `text` (rather than letting them fall
 * through to a no-op load) keeps the codeToHtml call's `lang` argument
 * aligned with what the highlighter actually knows about.
 */
const SUPPORTED_LANGS = new Set<string>([...Object.keys(LANG_LOADERS), 'text']);

export async function highlightCode(
  code: string,
  lang: string,
  theme: HighlightTheme,
): Promise<string> {
  const h = await getHighlighter();
  let effective = lang;
  if (effective !== 'text') {
    const loader = LANG_LOADERS[effective];
    if (!loader) {
      // Unknown language id — render as plain text rather than letting
      // codeToHtml throw on an unloaded grammar.
      effective = 'text';
    } else {
      try {
        await h.loadLanguage(loader() as Parameters<typeof h.loadLanguage>[0]);
      } catch {
        // Grammar import or registration failed — fall back to plain text.
        effective = 'text';
      }
    }
  }
  return h.codeToHtml(code, { lang: effective, theme });
}

const EXT_MAP: Record<string, string> = {
  ts: 'typescript',
  tsx: 'tsx',
  js: 'javascript',
  jsx: 'jsx',
  go: 'go',
  py: 'python',
  rb: 'ruby',
  rs: 'rust',
  java: 'java',
  c: 'c',
  cpp: 'cpp',
  h: 'c',
  yml: 'yaml',
  yaml: 'yaml',
  json: 'json',
  md: 'markdown',
  mod: 'go',
  // Extensions below have no grammar in LANG_LOADERS — they map to
  // 'text' so file viewers render them as plain text without trying
  // (and failing) to load a missing grammar. Keeping the entries
  // explicit documents what we recognise vs. what we highlight.
  sum: 'text',
  lock: 'text',
  txt: 'text',
  env: 'text',
  gitignore: 'text',
};

export function detectLanguage(filename: string): string {
  const lower = filename.toLowerCase();
  // Special-case filenames have no extension; resolve them first then
  // gate against SUPPORTED_LANGS so we never return an unsupported id.
  let candidate: string | undefined;
  if (lower === 'dockerfile' || lower === 'makefile' || lower === 'cmakelists.txt') {
    candidate = 'text';
  } else {
    const ext = lower.split('.').pop() || '';
    candidate = EXT_MAP[ext];
  }
  if (candidate && SUPPORTED_LANGS.has(candidate)) return candidate;
  return 'text';
}
