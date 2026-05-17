import type { ComponentPropsWithoutRef } from "react";
import { cn } from "@/lib/cn";

const VARIANTS = {
  live: "bg-success/15 text-success-ink border-success/30",
  watch: "bg-watch-soft text-warn-ink border-watch/30",
  danger: "bg-danger-soft text-danger border-transparent",
  muted: "bg-neutral text-text2 border-border",
  success: "bg-success-soft text-success-mid border-transparent",
} as const;

export type PillVariant = keyof typeof VARIANTS;

type PillProps = {
  variant?: PillVariant;
  className?: string;
} & ComponentPropsWithoutRef<"span">;

export function Pill({ variant = "muted", className, ...props }: PillProps) {
  return (
    <span
      className={cn(
        "inline-flex items-center px-2.5 py-1 rounded-full",
        "text-[11px] font-extrabold tracking-[0.08em] uppercase",
        "border",
        VARIANTS[variant],
        className,
      )}
      {...props}
    />
  );
}
