/**
 * Per-line blame viewer for Git files.
 * Shows commit attribution alongside syntax-highlighted code.
 * Uses DOMPurify-sanitized Shiki output (same defense-in-depth as FileViewer).
 */

import { useState, useEffect } from 'react';
import DOMPurify from 'dompurify';
import { ArrowLeft } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Skeleton } from '@/components/ui/skeleton';
import { highlightCode, detectLanguage, type HighlightTheme } from '@/lib/highlight';
import { formatDate } from '@/lib/format';
import { useGitBlame } from '@/api/queries';

interface BlameViewerProps {
  projectName: string;
  repoName: string;
  currentRef: string;
  filePath: string;
  onBack: () => void;
}

function escapeHtml(s: string): string {
  return s
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;');
}

export function BlameViewer({
  projectName,
  repoName,
  currentRef,
  filePath,
  onBack,
}: BlameViewerProps) {
  const { data: blameData, isLoading } = useGitBlame(
    projectName,
    repoName,
    currentRef,
    filePath,
  );
  // The async highlight result is stored together with the blame payload
  // it was computed from; `highlighting` and `highlightedLines` are then
  // derived by identity instead of being mirrored into state from inside
  // the effect body (no synchronous setState in the effect).
  const [highlightResult, setHighlightResult] = useState<{
    source: NonNullable<typeof blameData>;
    lines: string[];
  } | null>(null);

  useEffect(() => {
    if (!blameData?.lines?.length) {
      return;
    }

    let cancelled = false;

    const fullCode = blameData.lines.map((l) => l.content).join('\n');
    const lang = detectLanguage(filePath);
    const theme: HighlightTheme = document.documentElement.classList.contains('dark')
      ? 'github-dark'
      : 'github-light';

    highlightCode(fullCode, lang, theme)
      .then((html) => {
        if (cancelled) return;
        // Defense-in-depth: sanitize Shiki output through DOMPurify.
        // Same pattern as FileViewer -- Shiki output is grammar-tokenized, not
        // user HTML, but we sanitize as extra safety against regressions.
        const sanitized = DOMPurify.sanitize(html, {
          ALLOWED_TAGS: ['pre', 'code', 'span'],
          ALLOWED_ATTR: ['class', 'style'],
        });
        // Parse out individual lines from the highlighted HTML
        const parser = new DOMParser();
        const doc = parser.parseFromString(sanitized, 'text/html');
        const codeEl = doc.querySelector('code');
        if (codeEl) {
          const innerHtml = codeEl.innerHTML;
          const lines = innerHtml.split('\n');
          setHighlightResult({ source: blameData, lines });
        } else {
          setHighlightResult({
            source: blameData,
            lines: blameData.lines.map((l) => escapeHtml(l.content)),
          });
        }
      })
      .catch(() => {
        if (!cancelled) {
          setHighlightResult({
            source: blameData,
            lines: blameData.lines.map((l) => escapeHtml(l.content)),
          });
        }
      });

    return () => {
      cancelled = true;
    };
  }, [blameData, filePath]);

  // Derived: we are mid-highlight while there is blame data but the
  // stored result was computed from a different (stale or absent) payload.
  const resultIsCurrent =
    highlightResult !== null && highlightResult.source === blameData;
  const highlighting = !!blameData?.lines?.length && !resultIsCurrent;
  const highlightedLines = resultIsCurrent ? highlightResult.lines : [];

  if (isLoading || highlighting) {
    return (
      <div className="space-y-2">
        <div className="flex items-center gap-2">
          <Button variant="ghost" size="sm" onClick={onBack}>
            <ArrowLeft className="mr-1.5 size-4" />
            Back
          </Button>
          <span className="text-sm font-medium">Blame: {filePath}</span>
        </div>
        <div className="space-y-1">
          {Array.from({ length: 10 }).map((_, i) => (
            <Skeleton key={i} className="h-5 w-full" />
          ))}
        </div>
      </div>
    );
  }

  if (!blameData?.lines?.length) {
    return (
      <div className="space-y-4">
        <div className="flex items-center gap-2">
          <Button variant="ghost" size="sm" onClick={onBack}>
            <ArrowLeft className="mr-1.5 size-4" />
            Back
          </Button>
        </div>
        <p className="text-center text-sm text-muted-foreground">
          No blame data available for this file.
        </p>
      </div>
    );
  }

  // Group consecutive lines by SHA for alternating shading. Precomputed
  // as a pure pass so the JSX map below doesn't mutate render-scope
  // variables.
  const blockParity: boolean[] = [];
  {
    let prevSha = '';
    let blockIndex = 0;
    for (const line of blameData.lines) {
      if (line.sha !== prevSha) {
        blockIndex++;
        prevSha = line.sha;
      }
      blockParity.push(blockIndex % 2 === 0);
    }
  }

  return (
    <div className="space-y-3">
      <div className="flex items-center gap-2">
        <Button variant="ghost" size="sm" onClick={onBack}>
          <ArrowLeft className="mr-1.5 size-4" />
          Back
        </Button>
        <span className="text-sm font-medium">Blame: {filePath}</span>
      </div>

      <div className="overflow-x-auto rounded-lg border font-mono text-xs">
        <table className="w-full border-collapse">
          <tbody>
            {blameData.lines.map((line, idx) => {
              const isEvenBlock = blockParity[idx];
              const showBlameInfo = idx === 0 || blameData.lines[idx - 1].sha !== line.sha;

              // Each line's highlighted HTML was already DOMPurify-sanitized above
              const lineHtml = highlightedLines[idx] ?? escapeHtml(line.content);

              return (
                <tr
                  key={idx}
                  className={isEvenBlock ? 'bg-muted/20' : ''}
                >
                  {/* Blame attribution column */}
                  <td className="w-64 shrink-0 border-r px-2 py-0 align-top">
                    {showBlameInfo && (
                      <div className="flex items-center gap-2 whitespace-nowrap py-0.5">
                        <code className="text-muted-foreground">
                          {line.sha.slice(0, 7)}
                        </code>
                        <span className="max-w-24 truncate text-foreground">
                          {line.author}
                        </span>
                        <span className="text-muted-foreground">
                          {formatDate(line.date)}
                        </span>
                      </div>
                    )}
                  </td>
                  {/* Line number */}
                  <td className="w-10 shrink-0 select-none border-r px-2 py-0 text-right text-muted-foreground">
                    {line.line_number}
                  </td>
                  {/* Code line */}
                  <td className="px-2 py-0">
                    <span
                      className="leading-5"
                      // Safe: lineHtml is DOMPurify-sanitized Shiki grammar output
                      // or HTML-escaped plain text (escapeHtml fallback)
                      dangerouslySetInnerHTML={{ __html: lineHtml }}
                    />
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    </div>
  );
}
