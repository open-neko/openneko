import type { ComponentPropsWithoutRef } from "react";
import { cn } from "@/lib/cn";

type ButtonProps = {
  variant?: "primary" | "secondary";
  className?: string;
} & ComponentPropsWithoutRef<"button">;

export function Button({
  variant = "secondary",
  type = "button",
  className,
  children,
  ...props
}: ButtonProps) {
  return (
    <button
      type={type}
      className={cn(
        "inline-flex items-center px-4.5 py-2.5 rounded-full border-[1.5px] font-body text-[14.5px] font-medium cursor-pointer",
        "transition-[color,background,border-color,transform,box-shadow] duration-200",
        "disabled:opacity-55 disabled:cursor-not-allowed",
        variant === "secondary" &&
          "bg-white/60 border-border text-text2 not-disabled:hover:border-accent not-disabled:hover:text-accent not-disabled:hover:bg-accent-soft not-disabled:hover:-translate-y-px",
        variant === "primary" &&
          "bg-text border-text text-bg shadow-[0_2px_10px_rgba(20,18,12,0.18)]",
        className,
      )}
      {...props}
    >
      {variant === "primary" && (
        <span
          aria-hidden="true"
          className="inline-block w-1.5 h-1.5 rounded-full bg-success mr-2 shadow-[0_0_0_3px_rgba(108,255,127,0.18)]"
        />
      )}
      {children}
    </button>
  );
}
