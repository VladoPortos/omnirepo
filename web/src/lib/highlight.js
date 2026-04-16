/**
 * Shiki syntax highlighter with on-demand language loading per D-38.
 * Uses shiki/core to avoid bundling all grammars upfront.
 */
import { createHighlighterCore } from 'shiki/core';
import { createOnigurumaEngine } from 'shiki/engine/oniguruma';
let highlighterPromise = null;
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
export async function highlightCode(code, lang, theme) {
    const h = await getHighlighter();
    if (lang !== 'text') {
        try {
            await h.loadLanguage(await import(`@shikijs/langs/${lang}`));
        }
        catch {
            // Language grammar not found -- fall back to plain text
            lang = 'text';
        }
    }
    return h.codeToHtml(code, { lang, theme });
}
const EXT_MAP = {
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
    cs: 'csharp',
    sh: 'bash',
    bash: 'bash',
    yml: 'yaml',
    yaml: 'yaml',
    json: 'json',
    xml: 'xml',
    html: 'html',
    css: 'css',
    sql: 'sql',
    md: 'markdown',
    toml: 'toml',
    dockerfile: 'dockerfile',
    makefile: 'makefile',
    mod: 'go',
    sum: 'text',
    lock: 'text',
    txt: 'text',
    cfg: 'ini',
    ini: 'ini',
    env: 'text',
    gitignore: 'text',
};
export function detectLanguage(filename) {
    const lower = filename.toLowerCase();
    if (lower === 'dockerfile')
        return 'dockerfile';
    if (lower === 'makefile')
        return 'makefile';
    if (lower === 'cmakelists.txt')
        return 'cmake';
    const ext = lower.split('.').pop() || '';
    return EXT_MAP[ext] || 'text';
}
