/**
 * ErrorEnvelopeRenderer — renders an ApiErrorEnvelope per the
 * class-by-class contract.
 *
 * All text is rendered via standard JSX children (no raw-HTML injection
 * APIs are used anywhere in this file), so the server envelope is
 * treated as untrusted input and React's default escaping handles the
 * XSS surface.
 */

import { useEffect, useState } from 'react';
import {
  AlertCircle,
  Lock,
  RefreshCw,
  Wrench,
  type LucideIcon,
} from 'lucide-react';

import { Button } from '@/components/ui/button';
import { cn } from '@/lib/utils';
import {
  envelopeHasFieldPointer,
  type ApiErrorClass,
  type ApiErrorEnvelope,
} from '@/api/client';

import { CopyButton } from './CopyButton';

interface ClassStyle {
  icon: LucideIcon;
  /** bg + text + border utility classes referencing the status tokens. */
  colorClass: string;
  /** Standalone icon color (same foreground token as the block). */
  iconColorClass: string;
  role: 'alert' | 'status';
  ariaLive?: 'polite';
}

/**
 * classStyles maps ApiErrorClass → visual treatment. Uses the status-token
 * utility classes. Tailwind 4 does not fail compile on unknown utilities —
 * it simply no-ops until the tokens are declared (the status tokens live
 * in web/src/index.css).
 */
const classStyles: Record<ApiErrorClass, ClassStyle> = {
  validation: {
    icon: AlertCircle,
    colorClass:
      'bg-status-warning text-status-warning-foreground border-status-warning-border',
    iconColorClass: 'text-status-warning-foreground',
    role: 'status',
    ariaLive: 'polite',
  },
  permission: {
    icon: Lock,
    colorClass:
      'bg-status-failure text-status-failure-foreground border-status-failure-border',
    iconColorClass: 'text-status-failure-foreground',
    role: 'alert',
  },
  transient: {
    icon: RefreshCw,
    colorClass:
      'bg-status-warning text-status-warning-foreground border-status-warning-border',
    iconColorClass: 'text-status-warning-foreground',
    role: 'status',
    ariaLive: 'polite',
  },
  operator_action_required: {
    icon: Wrench,
    colorClass:
      'bg-status-maintenance text-status-maintenance-foreground border-status-maintenance-border',
    iconColorClass: 'text-status-maintenance-foreground',
    role: 'alert',
  },
};

/**
 * Default hints per class. Validation has two default hints: the copy
 * "Check the highlighted field." applies only when the envelope actually
 * carries a field pointer (details.field or details.fields). Validation
 * envelopes that are global — missing name, form-wide rule — fall back to
 * "Please review the form." so the user isn't told to look for a highlight
 * that isn't there. See envelopeHasFieldPointer in @/api/client.
 */
const defaultHints: Record<ApiErrorClass, string> = {
  validation: 'Please review the form.',
  permission: "You don't have access. Ask a project owner.",
  transient: 'Please try again.',
  operator_action_required: 'An administrator must fix this first.',
};
const validationHintWithFieldPointer = 'Check the highlighted field.';

/**
 * operatorDefaultLabels — fallback CTA label by code prefix when the
 * server omits details.operator_label. Order matters: first match wins.
 */
const operatorDefaultLabels: Array<[RegExp, string]> = [
  [/^trivy\./, 'Go to Admin → Trivy'],
  [/^tls\./, 'Go to Admin → TLS'],
  [/^gc\./, 'Go to Admin → Garbage Collection'],
  [/^maintenance\./, 'Go to Admin → Maintenance'],
];

function resolveOperatorLabel(envelope: ApiErrorEnvelope): string {
  if (envelope.details?.operator_label) return envelope.details.operator_label;
  for (const [re, label] of operatorDefaultLabels) {
    if (re.test(envelope.code)) return label;
  }
  return 'Open admin action';
}

interface ErrorEnvelopeRendererProps {
  envelope: ApiErrorEnvelope | null;
  onRetry?: () => void;
  mode?: 'inline' | 'page';
  className?: string;
}

export function ErrorEnvelopeRenderer({
  envelope,
  onRetry,
  mode = 'inline',
  className,
}: ErrorEnvelopeRendererProps) {
  if (!envelope) return null;
  const style = classStyles[envelope.class];
  const Icon = style.icon;
  const iconSize = mode === 'page' ? 'size-6' : 'size-4';
  const hint =
    envelope.hint ??
    (envelope.class === 'validation' && envelopeHasFieldPointer(envelope)
      ? validationHintWithFieldPointer
      : defaultHints[envelope.class]);

  return (
    <div
      role={style.role}
      aria-live={style.ariaLive}
      className={cn(
        'rounded-lg border p-4',
        mode === 'page' &&
          'mx-auto max-w-lg flex flex-col items-center text-center gap-3',
        style.colorClass,
        className,
      )}
      data-envelope-class={envelope.class}
    >
      <div
        className={cn(
          'flex items-start gap-2',
          mode === 'page' && 'flex-col items-center',
        )}
      >
        <Icon
          className={cn(iconSize, 'shrink-0 mt-0.5', style.iconColorClass)}
          aria-hidden="true"
        />
        <div className="flex-1">
          <p className="text-sm">{envelope.message}</p>
          {hint ? (
            <p className="text-xs text-muted-foreground mt-1">{hint}</p>
          ) : null}
        </div>
      </div>
      {envelope.class === 'transient' ? (
        <div className="mt-3">
          <TransientRetryButton envelope={envelope} onRetry={onRetry} />
        </div>
      ) : null}
      {envelope.class === 'operator_action_required' ? (
        <div className="mt-3">
          <OperatorDeepLinkButton envelope={envelope} />
        </div>
      ) : null}
      {envelope.incident_id ? (
        <div className="mt-3 flex items-center gap-2 text-xs text-muted-foreground font-mono">
          <span className="select-all">Incident {envelope.incident_id}</span>
          <CopyButton text={envelope.incident_id} />
        </div>
      ) : null}
    </div>
  );
}

function TransientRetryButton({
  envelope,
  onRetry,
}: {
  envelope: ApiErrorEnvelope;
  onRetry?: () => void;
}) {
  const retryAfterMs = envelope.details?.retry_after_ms ?? 0;
  const [remaining, setRemaining] = useState<number>(retryAfterMs);

  useEffect(() => {
    if (retryAfterMs <= 0) {
      setRemaining(0);
      return;
    }
    setRemaining(retryAfterMs);
    const timer = setInterval(() => {
      setRemaining((prev) => {
        if (prev <= 1000) {
          clearInterval(timer);
          return 0;
        }
        return prev - 1000;
      });
    }, 1000);
    return () => clearInterval(timer);
  }, [retryAfterMs]);

  const disabled = remaining > 0;
  const seconds = Math.ceil(remaining / 1000);

  return (
    <Button
      variant="outline"
      size="sm"
      disabled={disabled}
      onClick={() => onRetry?.()}
    >
      {disabled ? `Try again in ${seconds}s` : 'Try again'}
    </Button>
  );
}

function OperatorDeepLinkButton({
  envelope,
}: {
  envelope: ApiErrorEnvelope;
}) {
  const label = resolveOperatorLabel(envelope);
  const route = envelope.details?.operator_route ?? '/admin';
  return (
    <Button
      variant="default"
      size="sm"
      onClick={() => {
        // Same-origin SPA navigation only: operator_route is a path, not
        // a URL, and the air-gap invariant prevents any external URL here
        // from being functional even if it were injected.
        window.location.href = route;
      }}
    >
      {label}
    </Button>
  );
}
