import { jsx as _jsx, jsxs as _jsxs } from "react/jsx-runtime";
/**
 * Drag-and-drop upload area per D-15.
 * Dashed border, accent pulse on drag-over, per-file progress.
 */
import { useState, useCallback, useRef } from 'react';
import { Upload, CheckCircle, XCircle } from 'lucide-react';
import { Progress } from '@/components/ui/progress';
import { cn } from '@/lib/utils';
import { toast } from 'sonner';
export function Dropzone({ onUpload, accept, className }) {
    const [isDragOver, setIsDragOver] = useState(false);
    const [uploads, setUploads] = useState([]);
    const inputRef = useRef(null);
    const handleFiles = useCallback(async (files) => {
        const fileArray = Array.from(files);
        for (const file of fileArray) {
            const upload = { file, progress: 0, status: 'uploading' };
            setUploads((prev) => [...prev, upload]);
            try {
                await onUpload(file, (pct) => {
                    setUploads((prev) => prev.map((u) => u.file === file ? { ...u, progress: pct } : u));
                });
                setUploads((prev) => prev.map((u) => u.file === file ? { ...u, progress: 100, status: 'done' } : u));
                toast.success(`${file.name} uploaded successfully. Scan queued.`);
            }
            catch (err) {
                const message = err instanceof Error ? err.message : 'Unknown error';
                setUploads((prev) => prev.map((u) => u.file === file
                    ? { ...u, status: 'error', error: message }
                    : u));
                toast.error(`Failed to upload ${file.name}: ${message}`);
            }
        }
    }, [onUpload]);
    const handleDragOver = useCallback((e) => {
        e.preventDefault();
        setIsDragOver(true);
    }, []);
    const handleDragLeave = useCallback((e) => {
        e.preventDefault();
        setIsDragOver(false);
    }, []);
    const handleDrop = useCallback((e) => {
        e.preventDefault();
        setIsDragOver(false);
        if (e.dataTransfer.files.length > 0) {
            handleFiles(e.dataTransfer.files);
        }
    }, [handleFiles]);
    const handleClick = useCallback(() => {
        inputRef.current?.click();
    }, []);
    const handleInputChange = useCallback((e) => {
        if (e.target.files && e.target.files.length > 0) {
            handleFiles(e.target.files);
            e.target.value = '';
        }
    }, [handleFiles]);
    return (_jsxs("div", { className: cn('space-y-3', className), children: [_jsxs("div", { className: cn('flex min-h-[120px] cursor-pointer flex-col items-center justify-center gap-2 rounded-lg border-2 border-dashed p-6 transition-all duration-150', isDragOver
                    ? 'border-primary bg-primary/5 ring-2 ring-primary/20'
                    : 'border-muted-foreground/25 hover:border-muted-foreground/50'), onDragOver: handleDragOver, onDragLeave: handleDragLeave, onDrop: handleDrop, onClick: handleClick, role: "button", tabIndex: 0, onKeyDown: (e) => {
                    if (e.key === 'Enter' || e.key === ' ')
                        handleClick();
                }, children: [_jsx(Upload, { className: cn('size-8 transition-colors', isDragOver ? 'text-primary' : 'text-muted-foreground') }), _jsx("p", { className: "text-sm text-muted-foreground", children: isDragOver
                            ? 'Drop files to upload'
                            : 'Drag and drop files here, or click to browse' })] }), _jsx("input", { ref: inputRef, type: "file", className: "hidden", accept: accept, multiple: true, onChange: handleInputChange }), uploads.length > 0 && (_jsx("div", { className: "space-y-2", children: uploads.map((u, i) => (_jsxs("div", { className: "flex items-center gap-3 rounded-md border p-2 text-sm", children: [u.status === 'done' ? (_jsx(CheckCircle, { className: "size-4 shrink-0 text-green-500" })) : u.status === 'error' ? (_jsx(XCircle, { className: "size-4 shrink-0 text-destructive" })) : null, _jsxs("div", { className: "min-w-0 flex-1", children: [_jsx("p", { className: "truncate font-medium", children: u.file.name }), u.status === 'uploading' && (_jsx(Progress, { value: u.progress, className: "mt-1" })), u.status === 'error' && u.error && (_jsx("p", { className: "text-xs text-destructive", children: u.error }))] }), u.status === 'uploading' && (_jsxs("span", { className: "text-xs text-muted-foreground tabular-nums", children: [u.progress, "%"] }))] }, `${u.file.name}-${i}`))) }))] }));
}
