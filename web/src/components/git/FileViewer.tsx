/**
 * Syntax-highlighted file viewer for Git repositories.
 * Uses Shiki for highlighting with DOMPurify defense-in-depth.
 * Shows "File too large" for files > 1 MB.
 */

import { useState, useEffect } from 'react';
import DOMPurify from 'dompurify';
import { FileText, Download, Eye, ArrowLeft } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Skeleton } from '@/components/ui/skeleton';
import { CopyButton } from '@/components/common/CopyButton';
import { highlightCode, detectLanguage, type HighlightTheme } from '@/lib/highlight';
import type { GitFileContent } from '@/api/types';

const MAX_DISPLAY_SIZE = 1_048_576; // 1 MB

interface FileViewerProps {
  file: GitFileContent | undefined;
  loading?: boolean;
  onBlame?: () => void;
  onBack: () => void;
  downloadUrl?: string;
}

export function FileViewer({
  file,
  loading,
  onBlame,
  onBack,
  downloadUrl,
}: FileViewerProps) {
  const [html, setHtml] = useState('');
  const [highlighting, setHighlighting] = useState(false);

  useEffect(() => {
    if (!file?.content) {
      setHtml('');
      return;
    }

    if (file.size > MAX_DISPLAY_SIZE) {
      setHtml('');
      return;
    }

    let cancelled = false;
    setHighlighting(true);

    const content =
      file.encoding === 'base64'
        ? atob(file.content)
        : file.content;

    const lang = detectLanguage(file.name);
    const theme: HighlightTheme = document.documentElement.classList.contains('dark')
      ? 'github-dark'
      : 'github-light';

    highlightCode(content, lang, theme)
      .then((result) => {
        if (!cancelled) {
          // Defense-in-depth: sanitize Shiki output through DOMPurify.
          // Shiki produces only <pre>, <code>, <span> with inline styles from
          // grammar tokenization -- not user-supplied HTML. DOMPurify is an extra
          // safety layer against any future Shiki regression.
          const sanitized = DOMPurify.sanitize(result, {
            ALLOWED_TAGS: ['pre', 'code', 'span'],
            ALLOWED_ATTR: ['class', 'style'],
          });
          setHtml(sanitized);
        }
      })
      .catch(() => {
        if (!cancelled) {
          const escaped = content
            .replace(/&/g, '&amp;')
            .replace(/</g, '&lt;')
            .replace(/>/g, '&gt;');
          setHtml(`<pre><code>${escaped}</code></pre>`);
        }
      })
      .finally(() => {
        if (!cancelled) setHighlighting(false);
      });

    return () => {
      cancelled = true;
    };
  }, [file]);

  if (loading) {
    return (
      <div className="space-y-2">
        <Skeleton className="h-6 w-48" />
        <Skeleton className="h-64 w-full" />
      </div>
    );
  }

  if (!file) {
    return null;
  }

  // File too large guard
  if (file.size > MAX_DISPLAY_SIZE) {
    return (
      <div className="space-y-4">
        <div className="flex items-center gap-2">
          <Button variant="ghost" size="sm" onClick={onBack}>
            <ArrowLeft className="mr-1.5 size-4" />
            Back
          </Button>
        </div>
        <div className="flex flex-col items-center justify-center gap-4 rounded-lg border bg-muted/30 py-16">
          <FileText className="size-12 text-muted-foreground" />
          <p className="text-sm text-muted-foreground">
            File too large to display ({(file.size / 1_048_576).toFixed(1)} MB)
          </p>
          {downloadUrl && (
            <Button
              variant="outline"
              size="sm"
              render={<a href={downloadUrl} download={file.name} />}
            >
              <Download className="mr-1.5 size-4" />
              Download
            </Button>
          )}
        </div>
      </div>
    );
  }

  const rawContent =
    file.encoding === 'base64' ? atob(file.content) : file.content;
  const lineCount = rawContent.split('\n').length;

  return (
    <div className="space-y-3">
      {/* Header bar */}
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex items-center gap-2">
          <Button variant="ghost" size="sm" onClick={onBack}>
            <ArrowLeft className="mr-1.5 size-4" />
            Back
          </Button>
          <span className="text-sm font-medium">{file.path}</span>
          <span className="text-xs text-muted-foreground">
            {lineCount} lines
          </span>
        </div>
        <div className="flex items-center gap-1">
          <CopyButton text={rawContent} />
          {onBlame && (
            <Button variant="ghost" size="sm" onClick={onBlame}>
              <Eye className="mr-1.5 size-4" />
              Blame
            </Button>
          )}
          {downloadUrl && (
            <Button
              variant="ghost"
              size="sm"
              render={<a href={downloadUrl} download={file.name} />}
            >
              <Download className="mr-1.5 size-4" />
              Raw
            </Button>
          )}
        </div>
      </div>

      {/* Code content -- HTML is DOMPurify-sanitized Shiki output (see above) */}
      <div className="overflow-x-auto rounded-lg border">
        {highlighting ? (
          <div className="p-4">
            <Skeleton className="h-64 w-full" />
          </div>
        ) : (
          <div className="flex">
            {/* Line numbers */}
            <div className="shrink-0 select-none border-r bg-muted/30 px-3 py-3 text-right font-mono text-xs text-muted-foreground">
              {Array.from({ length: lineCount }, (_, i) => (
                <div key={i + 1} className="leading-5">
                  {i + 1}
                </div>
              ))}
            </div>
            {/* Highlighted code -- sanitized by DOMPurify with allowlist */}
            <div
              className="flex-1 overflow-x-auto p-3 [&_pre]:!m-0 [&_pre]:!bg-transparent [&_pre]:!p-0 [&_code]:leading-5"
              dangerouslySetInnerHTML={{ __html: html }}
            />
          </div>
        )}
      </div>
    </div>
  );
}
