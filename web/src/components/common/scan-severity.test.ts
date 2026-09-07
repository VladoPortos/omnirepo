import { describe, expect, it } from 'vitest';
import { renderToStaticMarkup } from 'react-dom/server';
import { createElement } from 'react';
import { SeverityStrip } from './ArtifactDetail';
import { ContentScanBadge } from './ContentScanBadge';

describe('unknown severity findings', () => {
  it('shows UNKNOWN findings rather than claiming a clean scan', () => {
    const html = renderToStaticMarkup(createElement(SeverityStrip, { status: 'done', counts: { unknown: 3 } }));
    expect(html).toContain('unknown');
    expect(html).toContain('3');
    expect(html).not.toContain('No vulnerabilities');
  });
  it('preserves the unknown content badge', () => {
    const html = renderToStaticMarkup(createElement(ContentScanBadge, { severity: 'unknown' }));
    expect(html.toLowerCase()).toContain('unknown');
    expect(html).not.toContain('Clean');
  });
});
