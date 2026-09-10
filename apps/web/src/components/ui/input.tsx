import { forwardRef, type ComponentPropsWithoutRef } from "react";
import { cn } from "@/lib/cn";

export const controlClassName =
  "w-full rounded-control border-[1.5px] border-border bg-card px-3.5 py-2.5 font-body text-ui-body text-text outline-none transition-[border-color,box-shadow,background-color] placeholder:text-text3 hover:border-text3 focus-visible:border-accent focus-visible:shadow-[0_0_0_3px_var(--focus-ring)] disabled:cursor-not-allowed disabled:bg-neutral-soft disabled:opacity-60 aria-invalid:border-danger";

export const Input = forwardRef<
  HTMLInputElement,
  ComponentPropsWithoutRef<"input">
>(function Input({ className, ...props }, ref) {
  return (
    <input
      ref={ref}
      data-slot="input"
      data-ui-field-control=""
      className={cn(controlClassName, className)}
      {...props}
    />
  );
});
