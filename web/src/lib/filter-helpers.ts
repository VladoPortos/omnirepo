/**
 * filter-helpers — shared CSV/array helpers for the FilterWidget*
 * components (APT, RPM, PyPI, Helm). Extracted from per-widget copies;
 * behavior is identical to the originals.
 */

// csvToArr splits a comma-separated user input into trimmed non-empty
// strings. "focal, jammy," → ["focal", "jammy"]. Empty string → [].
export function csvToArr(raw: string): string[] {
  return raw
    .split(',')
    .map((s) => s.trim())
    .filter((s) => s.length > 0);
}

export function arrToCsv(arr: string[] | undefined): string {
  return (arr ?? []).join(', ');
}

// setOrUndef emits `undefined` for empty arrays so JSON payloads omit
// the key entirely. The backend validator treats a missing key as
// "mirror everything in this dimension" — the same semantic as an
// empty array, but smaller on the wire and easier to grep for in logs.
export function setOrUndef<T>(arr: T[]): T[] | undefined {
  return arr.length === 0 ? undefined : arr;
}
