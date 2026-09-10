import { forwardRef, type ComponentPropsWithoutRef, type ReactNode } from "react";
import { cn } from "@/lib/cn";

const BUTTON_VARIANTS = {
  primary:
    "border-text bg-text text-bg shadow-[0_2px_10px_rgba(20,18,12,0.16)] hover:border-accent hover:bg-accent hover:text-white",
  secondary:
    "border-border bg-card/85 text-text2 shadow-soft hover:border-accent hover:bg-accent-soft hover:text-accent",
  ghost:
    "border-transparent bg-transparent text-text2 hover:bg-neutral-soft hover:text-text",
  danger:
    "border-danger/30 bg-danger-soft text-danger hover:border-danger hover:bg-danger hover:text-white",
} as const;

const BUTTON_SIZES = {
  sm: "min-h-8 gap-1.5 px-3 py-1.5 text-ui-body-sm leading-none",
  md: "min-h-10 gap-2 px-4 py-2 text-ui-body leading-none",
  lg: "min-h-11 gap-2 px-5 py-2.5 text-ui-body-lg leading-none",
  "icon-sm": "size-8 shrink-0 p-0 [&_svg]:size-3.5",
  icon: "size-10 shrink-0 p-0 [&_svg]:size-4",
} as const;

export type ButtonVariant = keyof typeof BUTTON_VARIANTS;
export type ButtonSize = keyof typeof BUTTON_SIZES;

type ButtonStyleOptions = {
  variant?: ButtonVariant;
  size?: ButtonSize;
  className?: string;
};

export function buttonClassName({
  variant = "secondary",
  size = "md",
  className,
}: ButtonStyleOptions = {}) {
  return cn(
    "ui-button inline-flex items-center justify-center whitespace-nowrap rounded-control border-[1.5px] font-body font-semibold cursor-pointer",
    "transition-[color,background-color,border-color,transform,box-shadow,opacity] duration-150",
    "hover:-translate-y-px active:translate-y-0",
    "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent focus-visible:ring-offset-2 focus-visible:ring-offset-bg",
    "disabled:cursor-not-allowed disabled:opacity-50 disabled:hover:translate-y-0",
    "aria-disabled:pointer-events-none aria-disabled:opacity-50",
    "[&_svg]:pointer-events-none [&_svg]:size-4 [&_svg]:shrink-0",
    BUTTON_VARIANTS[variant],
    BUTTON_SIZES[size],
    className,
  );
}

export type ButtonProps = {
  variant?: ButtonVariant;
  size?: ButtonSize;
} & ComponentPropsWithoutRef<"button">;

export const Button = forwardRef<HTMLButtonElement, ButtonProps>(
  function Button(
    {
      variant = "secondary",
      size = "md",
      type = "button",
      className,
      children,
      ...props
    },
    ref,
  ) {
    return (
      <button
        ref={ref}
        type={type}
        data-ui-button=""
        data-variant={variant}
        data-size={size}
        className={buttonClassName({ variant, size, className })}
        {...props}
      >
        {children}
      </button>
    );
  },
);

export type ButtonLinkProps = {
  variant?: ButtonVariant;
  size?: ButtonSize;
} & ComponentPropsWithoutRef<"a">;

export const ButtonLink = forwardRef<HTMLAnchorElement, ButtonLinkProps>(
  function ButtonLink(
    {
      variant = "secondary",
      size = "md",
      className,
      children,
      ...props
    },
    ref,
  ) {
    return (
      <a
        ref={ref}
        data-ui-button=""
        data-variant={variant}
        data-size={size}
        className={buttonClassName({ variant, size, className })}
        {...props}
      >
        {children}
      </a>
    );
  },
);

type IconButtonProps = Omit<ButtonProps, "aria-label" | "children" | "size"> & {
  label: string;
  size?: Extract<ButtonSize, "icon-sm" | "icon">;
  children: ReactNode;
};

export function IconButton({
  label,
  size = "icon",
  title,
  children,
  ...props
}: IconButtonProps) {
  return (
    <Button aria-label={label} title={title ?? label} size={size} {...props}>
      {children}
    </Button>
  );
}
