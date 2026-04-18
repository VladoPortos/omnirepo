/**
 * Button that copies text to clipboard with "Copied!" feedback via Tooltip.
 */

import { useState, useCallback } from 'react';
import { Copy, Check } from 'lucide-react';
import { Button } from '@/components/ui/button';
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip';

interface CopyButtonProps {
  text: string;
  className?: string;
  /**
   * Optional contextual aria-label override. Used by SnippetList to produce
   * per-snippet labels like "Copy Pull" / "Copy .pypirc" instead of the
   * generic default. Falls back to "Copy to clipboard" when unset.
   */
  'aria-label'?: string;
}

export function CopyButton({
  text,
  className,
  'aria-label': ariaLabel,
}: CopyButtonProps) {
  const [copied, setCopied] = useState(false);

  const handleCopy = useCallback(async () => {
    try {
      await navigator.clipboard.writeText(text);
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    } catch {
      // Fallback for non-secure contexts
      const textarea = document.createElement('textarea');
      textarea.value = text;
      textarea.style.position = 'fixed';
      textarea.style.opacity = '0';
      document.body.appendChild(textarea);
      textarea.select();
      document.execCommand('copy');
      document.body.removeChild(textarea);
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    }
  }, [text]);

  return (
    <>
      <Tooltip>
        <TooltipTrigger
          render={
            <Button
              variant="ghost"
              size="icon-sm"
              className={className}
              onClick={handleCopy}
              aria-label={ariaLabel ?? 'Copy to clipboard'}
            />
          }
        >
          {copied ? (
            <Check className="size-3.5 text-green-500" />
          ) : (
            <Copy className="size-3.5" />
          )}
        </TooltipTrigger>
        <TooltipContent>{copied ? 'Copied!' : 'Copy'}</TooltipContent>
      </Tooltip>
      {/* Phase 6: aria-live announcement so SR users hear copy success
          even when the Tooltip content never receives focus. */}
      <span aria-live="polite" aria-atomic="true" className="sr-only">
        {copied ? 'Copied to clipboard' : ''}
      </span>
    </>
  );
}
