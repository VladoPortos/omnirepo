import { jsx as _jsx, jsxs as _jsxs } from "react/jsx-runtime";
/**
 * Button that copies text to clipboard with "Copied!" feedback via Tooltip.
 */
import { useState, useCallback } from 'react';
import { Copy, Check } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Tooltip, TooltipContent, TooltipTrigger, } from '@/components/ui/tooltip';
export function CopyButton({ text, className }) {
    const [copied, setCopied] = useState(false);
    const handleCopy = useCallback(async () => {
        try {
            await navigator.clipboard.writeText(text);
            setCopied(true);
            setTimeout(() => setCopied(false), 2000);
        }
        catch {
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
    return (_jsxs(Tooltip, { children: [_jsx(TooltipTrigger, { render: _jsx(Button, { variant: "ghost", size: "icon-sm", className: className, onClick: handleCopy, "aria-label": "Copy to clipboard" }), children: copied ? (_jsx(Check, { className: "size-3.5 text-green-500" })) : (_jsx(Copy, { className: "size-3.5" })) }), _jsx(TooltipContent, { children: copied ? 'Copied!' : 'Copy' })] }));
}
