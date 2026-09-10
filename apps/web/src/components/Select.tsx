"use client";

import {
  Select as SelectRoot,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";

export type SelectOption = { value: string; label: string };

type Props = {
  value: string;
  onChange: (next: string) => void;
  options: readonly SelectOption[];
  disabled?: boolean;
  placeholder?: string;
  ariaLabel?: string;
  id?: string;
};

export default function Select({
  value,
  onChange,
  options,
  disabled,
  placeholder,
  ariaLabel,
  id,
}: Props) {
  return (
    <SelectRoot value={value} onValueChange={onChange} disabled={disabled}>
      <SelectTrigger
        id={id}
        aria-label={ariaLabel}
        data-ui-field-control=""
        className="min-h-10 w-full rounded-control border-[1.5px] border-border bg-card px-3.5 py-2.5 font-body text-ui-body text-text shadow-none hover:border-text3 focus-visible:border-accent focus-visible:ring-[3px] focus-visible:ring-[var(--focus-ring)]"
      >
        <SelectValue placeholder={placeholder} />
      </SelectTrigger>
      <SelectContent
        position="popper"
        className="z-50 max-h-80 min-w-[var(--radix-select-trigger-width)] rounded-inner border border-border bg-card p-1.5 shadow-lift"
      >
        {options.map((option) => (
          <SelectItem
            key={option.value}
            value={option.value}
            data-ui-menu-item=""
            className="min-h-10 rounded-[8px] px-3 py-2.5 font-body text-ui-body text-text focus:bg-accent-soft focus:text-accent"
          >
            {option.label}
          </SelectItem>
        ))}
      </SelectContent>
    </SelectRoot>
  );
}
