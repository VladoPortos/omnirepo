/**
 * formatFilterSummary — renders a compact human summary of a mirror
 * filter JSON for display beside the Sync now button. Non-throwing —
 * malformed JSON returns the empty string so the button still renders.
 *
 * Protocol-specific rendering:
 *   - deb:  "{suites} · {components} · {arches}"
 *   - rpm:  "{names}"
 *   - pypi: "{names}" or "{names} · {globs}"
 *   - helm: same as pypi
 */
export function formatFilterSummary(
  filterJSON: string,
  protocol: string,
): string | undefined {
  if (!filterJSON) return undefined;
  let obj: Record<string, unknown>;
  try {
    obj = JSON.parse(filterJSON);
  } catch {
    return undefined;
  }
  const parts: string[] = [];
  if (protocol === 'deb') {
    const suites = (obj.Suites as string[] | undefined) ?? [];
    const comps = (obj.Components as string[] | undefined) ?? [];
    const arches = (obj.Arches as string[] | undefined) ?? [];
    if (suites.length) parts.push(suites.join(', '));
    if (comps.length) parts.push(comps.join(', '));
    if (arches.length) parts.push(arches.join(', '));
  } else {
    const names = (obj.Names as string[] | undefined) ?? [];
    const globs = (obj.Globs as string[] | undefined) ?? [];
    if (names.length) parts.push(names.join(', '));
    if (globs.length) parts.push(`globs: ${globs.join(', ')}`);
  }
  return parts.length ? parts.join(' · ') : undefined;
}
