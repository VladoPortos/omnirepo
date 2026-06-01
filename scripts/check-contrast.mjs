#!/usr/bin/env node
// scripts/check-contrast.mjs
//
// Phase 6 / plan 06-08 — VISUAL-08 hard gate.
//
// Parses web/src/index.css, extracts the 18 `--status-*` OKLCH triplets
// (6 statuses x 3 suffixes: base / -foreground / -border) from the
// `:root` block, converts each to sRGB via the standard
// Oklch -> OKLab -> linear sRGB -> gamma sRGB pipeline (Bottosson 2020),
// computes WCAG 2.1 relative luminance, and derives contrast ratios.
//
// Hard assertions (exit 1 on any violation):
//   - For every status, contrast(--status-X-foreground, --status-X) >= 4.5
//     (WCAG AA for normal text on the Badge fill).
//
// Soft warnings (printed but non-fatal):
//   - contrast(--status-X-border, --background) — informational only;
//     the border exists to separate from card backgrounds, not to
//     carry text. WCAG has no formal threshold for this relationship.
//
// Pure Node. Zero npm deps. Invoked by `make check-contrast` which is
// wired into `make test`. Sister Playwright audit `web/e2e/a11y-audit.spec.ts`
// provides the broader WCAG AA breadth check via @axe-core/playwright.
//
// WCAG 2.1 relative-luminance + contrast formulas:
//   https://www.w3.org/WAI/GL/wiki/Relative_luminance
//   https://www.w3.org/TR/WCAG21/#contrast-minimum
// Oklch -> OKLab -> linear sRGB pipeline:
//   https://bottosson.github.io/posts/oklab/

import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, resolve } from 'node:path';

const __dirname = dirname(fileURLToPath(import.meta.url));
const CSS_PATH = resolve(__dirname, '..', 'web', 'src', 'index.css');

const STATUSES = [
  'healthy',
  'warning',
  'failure',
  'disabled',
  'maintenance',
  'neutral',
];

// WCAG AA contrast threshold for normal text (>= 4.5:1).
// Large text (18pt+ or 14pt+ bold) would be 3.0:1, but status-badge
// text is body-size so 4.5 is the correct gate.
const AA_TEXT_THRESHOLD = 4.5;

// ---------------------------------------------------------------------------
// CSS parsing — extract the `:root { ... }` block and pull oklch(L C H) values
// for each --status-* variable.
// ---------------------------------------------------------------------------

/**
 * Extract the body of the first `:root { ... }` block from a CSS file.
 * Parses brace-depth so nested rules (unlikely in plain CSS but cheap)
 * don't truncate the extraction.
 *
 * @param {string} css
 * @returns {string}
 */
function extractRootBlock(css) {
  const openRe = /:root\s*\{/g;
  const match = openRe.exec(css);
  if (!match) {
    throw new Error('check-contrast: could not find :root { block in CSS');
  }
  let depth = 1;
  let i = openRe.lastIndex;
  const start = i;
  while (i < css.length && depth > 0) {
    const ch = css[i];
    if (ch === '{') depth += 1;
    else if (ch === '}') depth -= 1;
    i += 1;
  }
  return css.slice(start, i - 1);
}

/**
 * Parse all `--name: oklch(L C H);` declarations from a block of CSS text.
 * Returns a Map from variable-name (no leading --) to {L, C, H}.
 *
 * Accepts floating-point L/C/H with optional exponent. The third argument
 * H is in degrees; OKLCH accepts any real number (wraps mod 360).
 *
 * @param {string} block
 * @returns {Map<string, {L:number, C:number, H:number}>}
 */
function parseOklchVars(block) {
  const re = /--([\w-]+)\s*:\s*oklch\(\s*([-\d.eE]+)\s+([-\d.eE]+)\s+([-\d.eE]+)\s*\)/g;
  const out = new Map();
  let m;
  while ((m = re.exec(block)) !== null) {
    const [, name, L, C, H] = m;
    out.set(name, { L: Number(L), C: Number(C), H: Number(H) });
  }
  return out;
}

// ---------------------------------------------------------------------------
// Oklch -> OKLab -> linear sRGB -> gamma sRGB — Bottosson 2020.
// ---------------------------------------------------------------------------

/**
 * Polar -> Cartesian in OKLab space.
 * @param {{L:number, C:number, H:number}} oklch — H in degrees
 * @returns {{L:number, a:number, b:number}}
 */
function oklchToOklab({ L, C, H }) {
  const hRad = (H * Math.PI) / 180;
  return {
    L,
    a: C * Math.cos(hRad),
    b: C * Math.sin(hRad),
  };
}

/**
 * OKLab -> linear sRGB. Matrix from Bottosson's reference implementation.
 * Output may be outside [0,1] for out-of-gamut colors; caller clamps.
 *
 * @param {{L:number, a:number, b:number}} lab
 * @returns {{r:number, g:number, b:number}} linear sRGB in [0..1] (approx)
 */
function oklabToLinearSrgb({ L, a, b }) {
  // Step 1: OKLab -> LMS (cube roots)
  const l_ = L + 0.3963377774 * a + 0.2158037573 * b;
  const m_ = L - 0.1055613458 * a - 0.0638541728 * b;
  const s_ = L - 0.0894841775 * a - 1.2914855480 * b;

  const l = l_ * l_ * l_;
  const m = m_ * m_ * m_;
  const s = s_ * s_ * s_;

  // Step 2: LMS -> linear sRGB
  return {
    r: 4.0767416621 * l - 3.3077115913 * m + 0.2309699292 * s,
    g: -1.2684380046 * l + 2.6097574011 * m - 0.3413193965 * s,
    b: -0.0041960863 * l - 0.7034186147 * m + 1.7076147010 * s,
  };
}

/**
 * Clamp to [0,1] then apply sRGB gamma companding.
 * @param {number} c linear channel
 * @returns {number} gamma channel in [0..1]
 */
function linearToSrgb(c) {
  const clamped = Math.max(0, Math.min(1, c));
  return clamped <= 0.0031308
    ? 12.92 * clamped
    : 1.055 * Math.pow(clamped, 1 / 2.4) - 0.055;
}

/**
 * WCAG 2.1 relative luminance (from sRGB [0..1]).
 * Applies the per-channel piecewise transform then 0.2126/0.7152/0.0722
 * weighting.
 *
 * @param {{r:number, g:number, b:number}} srgb channels in [0..1]
 * @returns {number} luminance in [0..1]
 */
function relativeLuminance({ r, g, b }) {
  const lin = (c) =>
    c <= 0.03928 ? c / 12.92 : Math.pow((c + 0.055) / 1.055, 2.4);
  return 0.2126 * lin(r) + 0.7152 * lin(g) + 0.0722 * lin(b);
}

/**
 * WCAG contrast ratio between two luminances.
 * @param {number} l1
 * @param {number} l2
 * @returns {number} ratio (>= 1)
 */
function contrast(l1, l2) {
  const [hi, lo] = l1 >= l2 ? [l1, l2] : [l2, l1];
  return (hi + 0.05) / (lo + 0.05);
}

/**
 * End-to-end: OKLCH triplet -> luminance in [0..1].
 * @param {{L:number, C:number, H:number}} oklch
 * @returns {number}
 */
function oklchToLuminance(oklch) {
  const lab = oklchToOklab(oklch);
  const linear = oklabToLinearSrgb(lab);
  const srgb = {
    r: linearToSrgb(linear.r),
    g: linearToSrgb(linear.g),
    b: linearToSrgb(linear.b),
  };
  return relativeLuminance(srgb);
}

// ---------------------------------------------------------------------------
// Main
// ---------------------------------------------------------------------------

function main() {
  const css = readFileSync(CSS_PATH, 'utf8');
  const rootBlock = extractRootBlock(css);
  const vars = parseOklchVars(rootBlock);

  // Also grab --background so the border/bg informational check has a reference.
  const bgOklch = vars.get('background');

  console.log('check-contrast: parsing', CSS_PATH);
  console.log(
    'check-contrast: found',
    vars.size,
    'oklch() variables in :root block',
  );
  console.log('');
  console.log('  status        text/fill    AA?    border/bg');
  console.log('  -------       ---------    ---    ---------');

  let failed = false;
  const missing = [];

  for (const status of STATUSES) {
    const fill = vars.get(`status-${status}`);
    const fg = vars.get(`status-${status}-foreground`);
    const border = vars.get(`status-${status}-border`);

    if (!fill || !fg || !border) {
      missing.push(status);
      failed = true;
      console.log(
        `  ${status.padEnd(13)} MISSING token(s): base=${!!fill} fg=${!!fg} border=${!!border}`,
      );
      continue;
    }

    const fillLum = oklchToLuminance(fill);
    const fgLum = oklchToLuminance(fg);
    const textFillRatio = contrast(fgLum, fillLum);
    const aaPass = textFillRatio >= AA_TEXT_THRESHOLD;

    let borderBgStr = '  (n/a)';
    if (bgOklch) {
      const bgLum = oklchToLuminance(bgOklch);
      const borderLum = oklchToLuminance(border);
      const borderBgRatio = contrast(borderLum, bgLum);
      borderBgStr = `${borderBgRatio.toFixed(2).padStart(5)}:1`;
    }

    const note = aaPass ? '' : `< ${AA_TEXT_THRESHOLD} — fails WCAG AA`;
    console.log(
      `  ${status.padEnd(13)} ${textFillRatio.toFixed(2).padStart(5)}:1    ${
        aaPass ? 'PASS' : 'FAIL'
      }   ${borderBgStr}      ${note}`,
    );

    if (!aaPass) failed = true;
  }

  console.log('');
  if (missing.length > 0) {
    console.error(
      'check-contrast: FAIL — missing tokens for:',
      missing.join(', '),
    );
  }
  if (failed) {
    console.error(
      'check-contrast: FAIL — one or more status tokens below WCAG AA (>=',
      AA_TEXT_THRESHOLD,
      ':1) for text on fill.',
    );
    console.error(
      '  Hand-tune OKLCH values in web/src/index.css (:root and .dark).',
    );
    console.error(
      '  Darken --status-*-foreground (decrease L), increase chroma if needed.',
    );
    process.exit(1);
  }
  console.log(
    'check-contrast: PASS —',
    STATUSES.length,
    'statuses meet WCAG AA for text/fill.',
  );
  process.exit(0);
}

main();
