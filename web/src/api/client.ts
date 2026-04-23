/**
 * API client for OmniRepo REST API.
 *
 * - Base URL: /api/v1/
 * - Cookie-based auth (credentials: 'include')
 * - 401 -> redirect to /login
 * - 503 with maintenance -> propagates for maintenance banner
 *
 * Phase 6 / plan 03: error bodies are now ApiErrorEnvelope (validation /
 * permission / transient / operator_action_required classes) — see
 * internal/httperr on the Go side. handleResponse parses that envelope,
 * falls back to a synthetic envelope when a stale server or middleware
 * returns the legacy {error, detail} shape, and hangs the parsed
 * envelope off ApiError so UI surfaces can render it via
 * ErrorEnvelopeRenderer.
 */

export type ApiErrorClass =
  | 'validation'
  | 'permission'
  | 'transient'
  | 'operator_action_required';

export interface ApiErrorDetails {
  field?: string;
  fields?: Record<string, string>;
  retry_after_ms?: number;
  operator_route?: string;
  operator_label?: string;
  [key: string]: unknown;
}

export interface ApiErrorEnvelope {
  code: string;
  message: string;
  hint?: string;
  class: ApiErrorClass;
  incident_id?: string;
  details?: ApiErrorDetails;
}

/**
 * isApiErrorEnvelope — runtime type guard against the wire shape. Accepts
 * unknown so it can be applied to `await res.json()` results directly.
 * Only the three required fields (code, message, class) are validated;
 * optional fields are treated as opaque and forwarded to consumers.
 */
export function isApiErrorEnvelope(v: unknown): v is ApiErrorEnvelope {
  if (typeof v !== 'object' || v === null) return false;
  const o = v as Record<string, unknown>;
  if (typeof o.code !== 'string' || typeof o.message !== 'string') return false;
  if (
    o.class !== 'validation' &&
    o.class !== 'permission' &&
    o.class !== 'transient' &&
    o.class !== 'operator_action_required'
  ) {
    return false;
  }
  return true;
}

/**
 * ApiError carries the full envelope returned by the server. The legacy
 * `.code` / `.detail` getters read from the envelope so existing callers
 * (LoginPage, ProjectsPage, ChangePasswordPage, …) keep compiling and
 * rendering their current strings while the UI migrates to
 * ErrorEnvelopeRenderer.
 */
export class ApiError extends Error {
  constructor(
    public readonly status: number,
    public readonly envelope: ApiErrorEnvelope,
  ) {
    super(envelope.message);
    this.name = 'ApiError';
  }

  /** Back-compat getter for callers that still read `err.code`. */
  get code(): string {
    return this.envelope.code;
  }

  /** Back-compat getter for callers that still read `err.detail`. */
  get detail(): string {
    return this.envelope.message;
  }
}

/**
 * localEnvelope builds a client-side envelope for UI surfaces that need
 * to show an error that never came from the server — local form
 * validation ("passwords do not match"), network failures ("unable to
 * reach the server"), or UX-substituted messages that mask the raw API
 * reason. Gives these paths the same ErrorEnvelopeRenderer visual
 * treatment as server envelopes without inventing a parallel renderer.
 */
export function localEnvelope(
  message: string,
  opts?: {
    class?: ApiErrorClass;
    code?: string;
    hint?: string;
    details?: ApiErrorDetails;
  },
): ApiErrorEnvelope {
  return {
    code: opts?.code ?? 'ui.local',
    message,
    class: opts?.class ?? 'validation',
    hint: opts?.hint,
    details: opts?.details,
  };
}

/**
 * envelopeFromError extracts an ApiErrorEnvelope from an unknown thrown
 * value. Returns the server envelope when the error is an ApiError;
 * otherwise synthesizes a transient-class envelope from the fallback
 * message. Useful in catch blocks that want one-liner handling.
 */
export function envelopeFromError(err: unknown, fallback: string): ApiErrorEnvelope {
  if (err instanceof ApiError) return err.envelope;
  return localEnvelope(fallback, { class: 'transient', code: 'ui.network' });
}

/**
 * fieldErrorsFromEnvelope lifts the field-level messages off a validation
 * envelope into a flat `Record<fieldId, message>` that forms can index
 * into to drive aria-invalid on the corresponding <Input>. Mirrors the
 * shape produced by useApiError but works against a plain envelope
 * (e.g. the local-state envelope from a failed mutation catch block)
 * where useApiError can't bind to a TanStack query/mutation handle.
 *
 * Returns an empty object for any non-validation class OR a validation
 * envelope that carries no field pointer — callers can always pass
 * `fieldErrors[id]` without null checks.
 */
export function fieldErrorsFromEnvelope(
  env: ApiErrorEnvelope | null | undefined,
): Record<string, string> {
  if (!env || env.class !== 'validation') return {};
  if (env.details?.fields) return env.details.fields;
  if (env.details?.field) return { [env.details.field]: env.message };
  return {};
}

/**
 * envelopeHasFieldPointer returns true when the envelope carries enough
 * information for a form to light up a specific <Input>. Used by the
 * envelope renderer to pick between the "Check the highlighted field."
 * default hint (field pointer present) and the generic "Please review
 * the form." fallback (no pointer — a standalone error without an
 * input to blame would otherwise invite the user to look for a
 * highlight that isn't there).
 */
export function envelopeHasFieldPointer(
  env: ApiErrorEnvelope | null | undefined,
): boolean {
  if (!env || env.class !== 'validation') return false;
  if (env.details?.field) return true;
  if (env.details?.fields && Object.keys(env.details.fields).length > 0) {
    return true;
  }
  return false;
}

/**
 * synthesizeEnvelope turns a non-envelope body (legacy {error, detail}
 * from a stale server, middleware still on the old shape, or a fetch
 * that produced no JSON at all) into a minimal valid envelope so the
 * UI never blank-screens on pre-migration payloads.
 *
 * @param status   HTTP status — used to pick validation vs transient class
 * @param body     Parsed body (or null if the body was unreadable)
 * @param fallback Text fallback for `message` (statusText, xhr.statusText)
 */
function synthesizeEnvelope(
  status: number,
  body: unknown,
  fallback: string,
): ApiErrorEnvelope {
  const b = body as { error?: unknown; detail?: unknown; message?: unknown } | null;
  const legacyError =
    b && typeof b.error === 'string' ? b.error : 'unknown';
  const legacyMessage =
    b && typeof b.detail === 'string'
      ? b.detail
      : b && typeof b.message === 'string'
        ? b.message
        : fallback || 'Request failed';
  return {
    code: 'legacy.' + legacyError,
    message: legacyMessage,
    class: status >= 500 ? 'transient' : 'validation',
  };
}

class ApiClient {
  private baseUrl = '/api/v1';

  async get<T>(path: string, params?: Record<string, string>): Promise<T> {
    const url = new URL(this.baseUrl + path, window.location.origin);
    if (params) {
      Object.entries(params).forEach(([k, v]) => url.searchParams.set(k, v));
    }
    const res = await fetch(url.toString(), { credentials: 'include' });
    return this.handleResponse<T>(res);
  }

  async post<T>(path: string, body?: unknown): Promise<T> {
    const res = await fetch(this.baseUrl + path, {
      method: 'POST',
      credentials: 'include',
      headers: body !== undefined ? { 'Content-Type': 'application/json' } : {},
      body: body !== undefined ? JSON.stringify(body) : undefined,
    });
    return this.handleResponse<T>(res);
  }

  async postForm<T>(path: string, form: FormData): Promise<T> {
    const res = await fetch(this.baseUrl + path, {
      method: 'POST',
      credentials: 'include',
      body: form,
    });
    return this.handleResponse<T>(res);
  }

  async patch<T>(path: string, body: unknown): Promise<T> {
    const res = await fetch(this.baseUrl + path, {
      method: 'PATCH',
      credentials: 'include',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    });
    return this.handleResponse<T>(res);
  }

  async del<T>(path: string): Promise<T> {
    const res = await fetch(this.baseUrl + path, {
      method: 'DELETE',
      credentials: 'include',
    });
    return this.handleResponse<T>(res);
  }

  async upload(
    path: string,
    file: File,
    onProgress?: (pct: number) => void,
  ): Promise<void> {
    return new Promise((resolve, reject) => {
      const xhr = new XMLHttpRequest();
      xhr.open('PUT', this.baseUrl + path);
      xhr.withCredentials = true;

      if (onProgress) {
        xhr.upload.addEventListener('progress', (e) => {
          if (e.lengthComputable) {
            onProgress(Math.round((e.loaded / e.total) * 100));
          }
        });
      }

      xhr.onload = () => {
        if (xhr.status === 401) {
          // Preserve existing redirect-to-login behavior — the envelope
          // is constructed for the thrown error only, not for redirect
          // logic (useAuth reads err.status, not the envelope).
          reject(
            new ApiError(401, {
              code: 'auth.unauthenticated',
              message: 'Unauthorized',
              class: 'permission',
            }),
          );
          return;
        }
        if (xhr.status >= 200 && xhr.status < 300) {
          resolve();
          return;
        }
        let parsed: unknown = null;
        try {
          parsed = JSON.parse(xhr.responseText);
        } catch {
          parsed = null;
        }
        if (isApiErrorEnvelope(parsed)) {
          reject(new ApiError(xhr.status, parsed));
          return;
        }
        reject(
          new ApiError(
            xhr.status,
            synthesizeEnvelope(xhr.status, parsed, xhr.statusText),
          ),
        );
      };

      xhr.onerror = () => reject(new Error('Network error'));
      xhr.send(file);
    });
  }

  private async handleResponse<T>(res: Response): Promise<T> {
    if (res.status === 401) {
      // Preserve existing redirect-to-login behavior — the envelope is
      // constructed only so thrown ApiError has a consistent shape for
      // any consumer that does render it (LoginPage reads err.status
      // today; moving to ErrorEnvelopeRenderer in plan 06-05+ requires
      // a real envelope here).
      throw new ApiError(401, {
        code: 'auth.unauthenticated',
        message: 'Unauthorized',
        class: 'permission',
      });
    }
    if (!res.ok) {
      const body = await res.json().catch(() => null);
      if (isApiErrorEnvelope(body)) {
        throw new ApiError(res.status, body);
      }
      // Legacy fallback — stale tab against new server, or a middleware
      // path that still emits {error, detail} during the migration
      // window. Wrapping keeps a stale browser tab from blank-screening.
      throw new ApiError(
        res.status,
        synthesizeEnvelope(res.status, body, res.statusText),
      );
    }
    if (res.status === 204) return undefined as T;
    return res.json();
  }
}

export const api = new ApiClient();
