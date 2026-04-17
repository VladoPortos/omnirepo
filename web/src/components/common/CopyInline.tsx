/**
 * CopyInline — inline code block with a right-aligned CopyButton.
 * For single short values (URLs, digests, API keys) where a full
 * SnippetPanel or OneTimeReveal would be overkill.
 *
 * Uses the 8px (right-2 top-2) inset per UI-SPEC §Spacing Exceptions.
 * The 6px carve-out is grandfathered ONLY to the two v1.0 files where
 * it already appears (SnippetPanel, OneTimeReveal); any new placement
 * MUST use 8px. Plan 08 greps new files for the 6px classes and fails.
 *
 * Props:
 *   value     Required. The string rendered and copied.
 *   label?    Optional screen-reader label (sr-only span).
 *   masked?   Default false. When true, renders up to 32 bullets instead
 *             of the raw value (still copies the real value). Masking
 *             reveals length (<=32) but not content (T-06-06-02 accept).
 *   className Extra utility classes on the outer container.
 */

import { CopyButton } from './CopyButton';
import { cn } from '@/lib/utils';

export interface CopyInlineProps {
  value: string;
  label?: string;
  masked?: boolean;
  className?: string;
}

export function CopyInline({
  value,
  label,
  masked = false,
  className,
}: CopyInlineProps) {
  const displayed = masked
    ? '\u2022'.repeat(Math.min(value.length, 32))
    : value;

  return (
    <div
      className={cn(
        'relative rounded-md bg-muted p-3 pr-10',
        className,
      )}
    >
      {label ? <span className="sr-only">{label}</span> : null}
      <code className="block break-all font-mono text-xs leading-relaxed select-all">
        {displayed}
      </code>
      <CopyButton text={value} className="absolute right-2 top-2" />
    </div>
  );
}
