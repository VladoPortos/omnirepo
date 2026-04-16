import { jsx as _jsx, jsxs as _jsxs } from "react/jsx-runtime";
/**
 * Dialog for API key / S3 key / password reveal per 05-UI-SPEC.
 * Secret cleared from React state on dialog close (T-05-06-02).
 */
import { useState, useCallback, useEffect } from 'react';
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription, DialogFooter, } from '@/components/ui/dialog';
import { Button } from '@/components/ui/button';
import { CopyButton } from './CopyButton';
import { AlertTriangle } from 'lucide-react';
export function OneTimeReveal({ open, onOpenChange, title, secret, warningText = 'This secret will only be shown once. Copy it now and store it securely.', }) {
    const [internalSecret, setInternalSecret] = useState(secret);
    // Sync secret when it changes (new reveal)
    useEffect(() => {
        if (secret) {
            setInternalSecret(secret);
        }
    }, [secret]);
    const handleClose = useCallback((nextOpen) => {
        if (!nextOpen) {
            // Clear secret from state on close (T-05-06-02)
            setInternalSecret('');
        }
        onOpenChange(nextOpen);
    }, [onOpenChange]);
    return (_jsx(Dialog, { open: open, onOpenChange: handleClose, children: _jsxs(DialogContent, { children: [_jsxs(DialogHeader, { children: [_jsx(DialogTitle, { children: title }), _jsx(DialogDescription, { children: _jsxs("span", { className: "inline-flex items-start gap-2 text-amber-600 dark:text-amber-400", children: [_jsx(AlertTriangle, { className: "mt-0.5 size-4 shrink-0" }), warningText] }) })] }), _jsxs("div", { className: "relative rounded-md bg-muted p-3 pr-10", children: [_jsx("code", { className: "block break-all font-mono text-sm leading-relaxed select-all", children: internalSecret }), _jsx(CopyButton, { text: internalSecret, className: "absolute right-1.5 top-1.5" })] }), _jsx(DialogFooter, { children: _jsx(Button, { onClick: () => handleClose(false), children: "Close" }) })] }) }));
}
