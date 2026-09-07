import type { TrivyDBPullStatus } from '@/api/types';

export interface PullRequestMarker {
  previousStartedAt?: string;
  expectedStartedAt?: string;
}

// The API emits UTC RFC3339Nano. Pad fractions before lexical comparison;
// Date.parse would discard sub-millisecond differences between attempts.
function normalizedMarker(value: string): string | null {
  const match = /^(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2})(?:\.(\d{1,9}))?Z$/.exec(value);
  return match ? `${match[1]}.${(match[2] ?? '').padEnd(9, '0')}Z` : null;
}

export function supersededPull(status: TrivyDBPullStatus | undefined, request: PullRequestMarker | null): boolean {
  if (!status?.started_at || !request?.expectedStartedAt) return false;
  const latest = normalizedMarker(status.started_at);
  const expected = normalizedMarker(request.expectedStartedAt);
  return latest !== null && expected !== null && latest > expected;
}

export function completedPull(
  status: TrivyDBPullStatus | undefined,
  previousState: TrivyDBPullStatus['state'] | undefined,
  request: PullRequestMarker | null,
): boolean {
  if (status?.state !== 'success' && status?.state !== 'failure') return false;
  if (request) {
    return !!status.started_at && (request.expectedStartedAt
      ? status.started_at === request.expectedStartedAt || supersededPull(status, request)
      : status.started_at !== request.previousStartedAt);
  }
  return previousState === 'running';
}
