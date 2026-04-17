/**
 * Dev-only story page exercising ErrorEnvelopeRenderer against every
 * ApiErrorClass in both inline and page modes, and against a live wire
 * fetch of the backend's /api/v1/_dev/error/<class> routes. Used by
 * Playwright suites from plan 06-04 and by humans for visual review.
 *
 * Route registration in web/src/App.tsx is gated behind
 * import.meta.env.DEV so Vite tree-shakes this entire module out of
 * production bundles (T-06-03-04).
 *
 * Phase 6 / plan 03 task 3.
 */

import { useEffect, useState } from 'react';

import { ErrorEnvelopeRenderer } from '@/components/common/ErrorEnvelope';
import type { ApiErrorClass, ApiErrorEnvelope } from '@/api/client';

const CLASSES: ApiErrorClass[] = [
  'validation',
  'permission',
  'transient',
  'operator_action_required',
];

const canned: Record<ApiErrorClass, ApiErrorEnvelope> = {
  validation: {
    code: 'dev.validation',
    message: 'Some fields need your attention.',
    class: 'validation',
    details: { field: 'user.email', fields: { 'user.email': 'invalid' } },
    incident_id: '01937a00-0000-7000-8000-000000000001',
  },
  permission: {
    code: 'dev.permission',
    message: "You don't have permission to view this.",
    hint: 'Ask a project member to add you, or switch to a project where you have access.',
    class: 'permission',
    incident_id: '01937a00-0000-7000-8000-000000000002',
  },
  transient: {
    code: 'dev.transient',
    message: "We couldn't reach the server.",
    hint: 'Please try again in a few seconds.',
    class: 'transient',
    details: { retry_after_ms: 3000 },
    incident_id: '01937a00-0000-7000-8000-000000000003',
  },
  operator_action_required: {
    code: 'trivy.db_missing',
    message: 'OmniRepo needs an administrator to finish setup.',
    class: 'operator_action_required',
    details: {
      operator_route: '/admin/trivy',
      operator_label: 'Go to Admin → Trivy',
    },
    incident_id: '01937a00-0000-7000-8000-000000000004',
  },
};

export function ErrorClassStoryPage() {
  const [retryCount, setRetryCount] = useState(0);
  const [liveEnvelopes, setLiveEnvelopes] = useState<
    Partial<Record<ApiErrorClass, ApiErrorEnvelope>>
  >({});
  const [liveErrors, setLiveErrors] = useState<Partial<Record<ApiErrorClass, string>>>(
    {},
  );

  useEffect(() => {
    (async () => {
      const entries = await Promise.all(
        CLASSES.map(async (c) => {
          try {
            const res = await fetch(`/api/v1/_dev/error/${c}`);
            const body = (await res.json()) as ApiErrorEnvelope;
            return { class: c, body } as const;
          } catch (err) {
            return {
              class: c,
              error: err instanceof Error ? err.message : String(err),
            } as const;
          }
        }),
      );
      const gotEnvelopes: Partial<Record<ApiErrorClass, ApiErrorEnvelope>> = {};
      const gotErrors: Partial<Record<ApiErrorClass, string>> = {};
      for (const entry of entries) {
        if ('body' in entry) {
          gotEnvelopes[entry.class] = entry.body;
        } else {
          gotErrors[entry.class] = entry.error;
        }
      }
      setLiveEnvelopes(gotEnvelopes);
      setLiveErrors(gotErrors);
    })();
  }, []);

  return (
    <div className="p-8 space-y-12">
      <div className="space-y-1">
        <h1 className="text-2xl font-semibold tracking-tight">
          Error envelope story
        </h1>
        <p className="text-xs text-muted-foreground">
          Dev-only — exercises ErrorEnvelopeRenderer across every ApiErrorClass.
          Hit while OMNIREPO_DEV=1 for the live-wire section to populate.
        </p>
      </div>

      <section>
        <h2 className="text-lg font-semibold mb-4">Canned — inline mode</h2>
        <div className="grid gap-4 xl:grid-cols-2">
          {CLASSES.map((c) => (
            <div
              key={`inline-${c}`}
              data-story-class={c}
              data-story-mode="inline"
            >
              <h3 className="text-sm font-semibold mb-2">{c}</h3>
              <ErrorEnvelopeRenderer
                envelope={canned[c]}
                mode="inline"
                onRetry={() => setRetryCount((n) => n + 1)}
              />
            </div>
          ))}
        </div>
      </section>

      <section>
        <h2 className="text-lg font-semibold mb-4">Canned — page mode</h2>
        <div className="grid gap-8">
          {CLASSES.map((c) => (
            <div
              key={`page-${c}`}
              data-story-class={c}
              data-story-mode="page"
            >
              <h3 className="text-sm font-semibold mb-2">{c}</h3>
              <ErrorEnvelopeRenderer envelope={canned[c]} mode="page" />
            </div>
          ))}
        </div>
      </section>

      <section>
        <h2 className="text-lg font-semibold mb-4">
          Live wire — GET /api/v1/_dev/error/&lt;class&gt;
        </h2>
        <div className="grid gap-4 xl:grid-cols-2">
          {CLASSES.map((c) => (
            <div
              key={`live-${c}`}
              data-story-class={c}
              data-story-mode="live"
            >
              <h3 className="text-sm font-semibold mb-2">{c}</h3>
              {liveErrors[c] ? (
                <p className="text-xs text-muted-foreground">
                  Fetch failed: {liveErrors[c]}
                </p>
              ) : (
                <ErrorEnvelopeRenderer
                  envelope={liveEnvelopes[c] ?? null}
                  mode="inline"
                  onRetry={() => setRetryCount((n) => n + 1)}
                />
              )}
            </div>
          ))}
        </div>
      </section>

      <p className="text-xs text-muted-foreground" data-story-retry-count>
        retry clicks: {retryCount}
      </p>
    </div>
  );
}

export default ErrorClassStoryPage;
