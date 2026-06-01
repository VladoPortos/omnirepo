/**
 * useApiError — normalises a TanStack Query or Mutation result into the
 * shape ErrorEnvelopeRenderer consumes.
 *
 * Returns an empty/no-op state when the query has no error or the error
 * is not an ApiError — components can safely destructure without null
 * checks on the hook side.
 */

import type { UseMutationResult, UseQueryResult } from '@tanstack/react-query';

import { ApiError, type ApiErrorEnvelope } from '@/api/client';

export interface ApiErrorState {
  /** Parsed envelope, or null when there is no ApiError to render. */
  envelope: ApiErrorEnvelope | null;
  /** True only for transient-class errors — drives the Try again CTA. */
  isRetryable: boolean;
  /** Replays the request (refetch for queries, mutate for mutations). */
  retry: () => void;
  /**
   * Field-level messages keyed by dot-path, always a record (possibly
   * empty). Prefers envelope.details.fields (multi-field), falls back to
   * {[envelope.details.field]: envelope.message} (single-field
   * shortcut). Empty for non-validation classes.
   */
  fieldErrors: Record<string, string>;
  /** Convenience copy of envelope.incident_id, or null if absent. */
  incidentId: string | null;
}

/**
 * QueryLike accepts both UseQueryResult and UseMutationResult so the
 * hook can be called on either side of the TanStack Query API. Both
 * shapes carry an optional `error` field; only the retry path
 * distinguishes them.
 */
type QueryLike =
  | UseQueryResult<unknown, unknown>
  | UseMutationResult<unknown, unknown, unknown, unknown>;

const EMPTY: ApiErrorState = {
  envelope: null,
  isRetryable: false,
  retry: () => {
    // no-op — caller has no error to retry against
  },
  fieldErrors: {},
  incidentId: null,
};

export function useApiError(query: QueryLike): ApiErrorState {
  const error = (query as { error?: unknown }).error;
  if (!(error instanceof ApiError)) {
    return EMPTY;
  }
  const env = error.envelope;

  const fieldErrors: Record<string, string> =
    env.class === 'validation'
      ? (env.details?.fields ??
          (env.details?.field ? { [env.details.field]: env.message } : {}))
      : {};

  const retry = () => {
    // Queries expose refetch(); mutations expose mutate() + the last
    // variables. Prefer refetch when both surfaces are present on the
    // object (happens on UseQueryResult, which never has `mutate`).
    if ('refetch' in query && typeof (query as UseQueryResult).refetch === 'function') {
      void (query as UseQueryResult).refetch();
      return;
    }
    if ('mutate' in query) {
      const mut = query as UseMutationResult<unknown, unknown, unknown, unknown>;
      if ('variables' in mut) {
        mut.mutate(mut.variables as never);
      }
    }
  };

  return {
    envelope: env,
    isRetryable: env.class === 'transient',
    retry,
    fieldErrors,
    incidentId: env.incident_id ?? null,
  };
}
