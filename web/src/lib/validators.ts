// Client-side input sanity checks. Pair with server-side validation —
// these exist only to gate the submit button so users see immediate
// feedback on obviously-invalid values. The server is the source of
// truth for final correctness.

// bucketNameSeemsValid enforces the length bounds the helper copy
// advertises ("3–63 chars") in the Create S3 Bucket dialog. Character-set
// rules (lowercase, digits, hyphens, dots) remain server-authoritative to
// avoid confusing users mid-typing.
export function bucketNameSeemsValid(raw: string): boolean {
  const trimmed = raw.trim();
  return trimmed.length >= 3 && trimmed.length <= 63;
}
