import { jsx as _jsx } from "react/jsx-runtime";
/**
 * Clickable filter chips/buttons per D-06, D-47.
 * Multi-select toggle behavior.
 */
import { Toggle } from '@/components/ui/toggle';
import { cn } from '@/lib/utils';
export function FilterChips({ options, selected, onChange, className, }) {
    const handleToggle = (value) => {
        if (selected.includes(value)) {
            onChange(selected.filter((v) => v !== value));
        }
        else {
            onChange([...selected, value]);
        }
    };
    return (_jsx("div", { className: cn('flex flex-wrap gap-1.5', className), children: options.map((option) => (_jsx(Toggle, { pressed: selected.includes(option.value), onPressedChange: () => handleToggle(option.value), size: "sm", className: "h-7 px-2.5 text-xs", children: option.label }, option.value))) }));
}
