import { jsx as _jsx, jsxs as _jsxs } from "react/jsx-runtime";
/**
 * Input field for per-repo inline filtering per D-06.
 * Client-side filtering on keypress (no debounce).
 */
import { Search } from 'lucide-react';
import { Input } from '@/components/ui/input';
export function InlineSearch({ value, onChange, placeholder = 'Filter by name...', className, }) {
    return (_jsx("div", { className: className, children: _jsxs("div", { className: "relative", children: [_jsx(Search, { className: "absolute left-2.5 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" }), _jsx(Input, { value: value, onChange: (e) => onChange(e.target.value), placeholder: placeholder, className: "pl-8" })] }) }));
}
