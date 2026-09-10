import { Search } from "lucide-react";
import type { ComponentPropsWithoutRef } from "react";
import { Input } from "@/components/ui/Field";
import { cn } from "@/lib/cn";

type SearchInputProps = Omit<ComponentPropsWithoutRef<"input">, "type"> & {
  label: string;
};

export function SearchInput({ label, className, ...props }: SearchInputProps) {
  return (
    <label className="relative block min-w-0" data-ui-list-search="">
      <span className="sr-only">{label}</span>
      <Search
        aria-hidden="true"
        className="pointer-events-none absolute left-3.5 top-1/2 z-10 size-4 -translate-y-1/2 text-text3"
      />
      <Input
        type="search"
        aria-label={label}
        className={cn("pl-10", className)}
        {...props}
      />
    </label>
  );
}
