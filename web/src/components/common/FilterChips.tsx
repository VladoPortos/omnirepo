/**
 * Clickable filter chips/buttons.
 * Multi-select toggle behavior.
 */

import { Toggle } from '@/components/ui/toggle';
import { cn } from '@/lib/utils';

interface FilterOption {
  label: string;
  value: string;
}

interface FilterChipsProps {
  options: FilterOption[];
  selected: string[];
  onChange: (selected: string[]) => void;
  className?: string;
}

export function FilterChips({
  options,
  selected,
  onChange,
  className,
}: FilterChipsProps) {
  const handleToggle = (value: string) => {
    if (selected.includes(value)) {
      onChange(selected.filter((v) => v !== value));
    } else {
      onChange([...selected, value]);
    }
  };

  return (
    <div className={cn('flex flex-wrap gap-1.5', className)}>
      {options.map((option) => (
        <Toggle
          key={option.value}
          pressed={selected.includes(option.value)}
          onPressedChange={() => handleToggle(option.value)}
          size="sm"
          className="h-7 px-2.5 text-xs"
        >
          {option.label}
        </Toggle>
      ))}
    </div>
  );
}
