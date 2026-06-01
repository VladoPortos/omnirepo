/**
 * Drag-and-drop upload area.
 * Dashed border, accent pulse on drag-over, per-file progress.
 */

import { useState, useCallback, useRef, type DragEvent } from 'react';
import { Upload, CheckCircle, XCircle } from 'lucide-react';
import { Progress } from '@/components/ui/progress';
import { cn } from '@/lib/utils';
import { toast } from 'sonner';

interface FileUpload {
  file: File;
  progress: number;
  status: 'uploading' | 'done' | 'error';
  error?: string;
}

interface DropzoneProps {
  onUpload: (file: File, onProgress: (pct: number) => void) => Promise<void>;
  accept?: string;
  className?: string;
}

export function Dropzone({ onUpload, accept, className }: DropzoneProps) {
  const [isDragOver, setIsDragOver] = useState(false);
  const [uploads, setUploads] = useState<FileUpload[]>([]);
  const inputRef = useRef<HTMLInputElement>(null);

  const handleFiles = useCallback(
    async (files: FileList | File[]) => {
      const fileArray = Array.from(files);
      for (const file of fileArray) {
        const upload: FileUpload = { file, progress: 0, status: 'uploading' };
        setUploads((prev) => [...prev, upload]);

        try {
          await onUpload(file, (pct) => {
            setUploads((prev) =>
              prev.map((u) =>
                u.file === file ? { ...u, progress: pct } : u,
              ),
            );
          });
          setUploads((prev) =>
            prev.map((u) =>
              u.file === file ? { ...u, progress: 100, status: 'done' } : u,
            ),
          );
          toast.success(`${file.name} uploaded successfully. Scan queued.`);
        } catch (err) {
          const message =
            err instanceof Error ? err.message : 'Unknown error';
          setUploads((prev) =>
            prev.map((u) =>
              u.file === file
                ? { ...u, status: 'error', error: message }
                : u,
            ),
          );
          toast.error(`Failed to upload ${file.name}: ${message}`);
        }
      }
    },
    [onUpload],
  );

  const handleDragOver = useCallback((e: DragEvent) => {
    e.preventDefault();
    setIsDragOver(true);
  }, []);

  const handleDragLeave = useCallback((e: DragEvent) => {
    e.preventDefault();
    setIsDragOver(false);
  }, []);

  const handleDrop = useCallback(
    (e: DragEvent) => {
      e.preventDefault();
      setIsDragOver(false);
      if (e.dataTransfer.files.length > 0) {
        handleFiles(e.dataTransfer.files);
      }
    },
    [handleFiles],
  );

  const handleClick = useCallback(() => {
    inputRef.current?.click();
  }, []);

  const handleInputChange = useCallback(
    (e: React.ChangeEvent<HTMLInputElement>) => {
      if (e.target.files && e.target.files.length > 0) {
        handleFiles(e.target.files);
        e.target.value = '';
      }
    },
    [handleFiles],
  );

  return (
    <div className={cn('space-y-3', className)}>
      <div
        className={cn(
          'flex min-h-[120px] cursor-pointer flex-col items-center justify-center gap-2 rounded-lg border-2 border-dashed p-6 transition-all duration-150',
          isDragOver
            ? 'border-primary bg-primary/5 ring-2 ring-primary/20'
            : 'border-muted-foreground/25 hover:border-muted-foreground/50',
        )}
        onDragOver={handleDragOver}
        onDragLeave={handleDragLeave}
        onDrop={handleDrop}
        onClick={handleClick}
        role="button"
        tabIndex={0}
        onKeyDown={(e) => {
          if (e.key === 'Enter' || e.key === ' ') handleClick();
        }}
      >
        <Upload
          className={cn(
            'size-8 transition-colors',
            isDragOver ? 'text-primary' : 'text-muted-foreground',
          )}
        />
        <p className="text-sm text-muted-foreground">
          {isDragOver
            ? 'Drop files to upload'
            : 'Drag and drop files here, or click to browse'}
        </p>
      </div>

      <input
        ref={inputRef}
        type="file"
        className="hidden"
        accept={accept}
        multiple
        onChange={handleInputChange}
      />

      {uploads.length > 0 && (
        <div className="space-y-2">
          {uploads.map((u, i) => (
            <div
              key={`${u.file.name}-${i}`}
              className="flex items-center gap-3 rounded-md border p-2 text-sm"
            >
              {u.status === 'done' ? (
                <CheckCircle className="size-4 shrink-0 text-green-500" />
              ) : u.status === 'error' ? (
                <XCircle className="size-4 shrink-0 text-destructive" />
              ) : null}
              <div className="min-w-0 flex-1">
                <p className="truncate font-medium">{u.file.name}</p>
                {u.status === 'uploading' && (
                  <Progress value={u.progress} className="mt-1" />
                )}
                {u.status === 'error' && u.error && (
                  <p className="text-xs text-destructive">{u.error}</p>
                )}
              </div>
              {u.status === 'uploading' && (
                <span className="text-xs text-muted-foreground tabular-nums">
                  {u.progress}%
                </span>
              )}
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
