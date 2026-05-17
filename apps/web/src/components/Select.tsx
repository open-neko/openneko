"use client";

import { useEffect, useId, useRef, useState } from "react";
import { cn } from "@/lib/cn";

export type SelectOption = {
  value: string;
  label: string;
};

type Props = {
  value: string;
  onChange: (next: string) => void;
  options: readonly SelectOption[];
  disabled?: boolean;
  placeholder?: string;
  ariaLabel?: string;
  id?: string;
};

const TRIGGER_BASE =
  "px-3.5 py-3 rounded-xl border-[1.5px] border-border bg-bg text-text text-[15px] font-body outline-none transition-all duration-200 focus:border-accent focus:shadow-[0_0_0_3px_rgba(107,92,231,0.08)]";

export default function Select({
  value,
  onChange,
  options,
  disabled,
  placeholder,
  ariaLabel,
  id,
}: Props) {
  const [open, setOpen] = useState(false);
  const [focusIdx, setFocusIdx] = useState<number>(() =>
    Math.max(0, options.findIndex((o) => o.value === value)),
  );
  const wrapRef = useRef<HTMLDivElement>(null);
  const buttonRef = useRef<HTMLButtonElement>(null);
  const listRef = useRef<HTMLUListElement>(null);
  const listboxId = useId();

  const selected = options.find((o) => o.value === value);

  useEffect(() => {
    if (!open) return;
    const onDocMouseDown = (e: MouseEvent) => {
      if (wrapRef.current && !wrapRef.current.contains(e.target as Node)) {
        setOpen(false);
      }
    };
    document.addEventListener("mousedown", onDocMouseDown);
    return () => document.removeEventListener("mousedown", onDocMouseDown);
  }, [open]);

  useEffect(() => {
    if (!open) return;
    const el = listRef.current?.querySelector<HTMLLIElement>(
      `[data-idx="${focusIdx}"]`,
    );
    el?.scrollIntoView({ block: "nearest" });
  }, [open, focusIdx]);

  const openWithCurrent = () => {
    setOpen(true);
    const cur = options.findIndex((o) => o.value === value);
    setFocusIdx(cur >= 0 ? cur : 0);
  };

  const select = (idx: number) => {
    const opt = options[idx];
    if (!opt) return;
    onChange(opt.value);
    setOpen(false);
    buttonRef.current?.focus();
  };

  const onKey = (e: React.KeyboardEvent) => {
    if (disabled) return;
    if (!open) {
      if (["Enter", " ", "ArrowDown", "ArrowUp"].includes(e.key)) {
        e.preventDefault();
        openWithCurrent();
      }
      return;
    }
    if (e.key === "Escape") {
      e.preventDefault();
      setOpen(false);
      buttonRef.current?.focus();
    } else if (e.key === "ArrowDown") {
      e.preventDefault();
      setFocusIdx((i) => Math.min(options.length - 1, i + 1));
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      setFocusIdx((i) => Math.max(0, i - 1));
    } else if (e.key === "Home") {
      e.preventDefault();
      setFocusIdx(0);
    } else if (e.key === "End") {
      e.preventDefault();
      setFocusIdx(options.length - 1);
    } else if (e.key === "Enter" || e.key === " ") {
      e.preventDefault();
      select(focusIdx);
    } else if (e.key === "Tab") {
      setOpen(false);
    }
  };

  return (
    <div ref={wrapRef} className="relative">
      <button
        ref={buttonRef}
        id={id}
        type="button"
        className={cn(
          TRIGGER_BASE,
          "flex items-center justify-between gap-2.5 w-full text-left cursor-pointer",
          "enabled:hover:border-accent",
          "disabled:cursor-not-allowed disabled:opacity-55",
        )}
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-controls={open ? listboxId : undefined}
        aria-label={ariaLabel}
        disabled={disabled}
        onClick={() => !disabled && (open ? setOpen(false) : openWithCurrent())}
        onKeyDown={onKey}
      >
        <span
          className={cn(
            "flex-1 whitespace-nowrap overflow-hidden text-ellipsis",
            !selected && "text-text3",
          )}
        >
          {selected?.label ?? placeholder ?? ""}
        </span>
        <svg
          className={cn(
            "flex-shrink-0 transition-transform duration-[180ms] ease-out",
            open ? "rotate-180 text-accent" : "text-text2",
          )}
          width="12"
          height="8"
          viewBox="0 0 12 8"
          aria-hidden="true"
        >
          <path
            d="M1 1l5 5 5-5"
            stroke="currentColor"
            strokeWidth="1.8"
            fill="none"
            strokeLinecap="round"
            strokeLinejoin="round"
          />
        </svg>
      </button>
      {open && (
        <ul
          ref={listRef}
          id={listboxId}
          role="listbox"
          tabIndex={-1}
          className="select-panel absolute z-30 top-[calc(100%+6px)] left-0 right-0 m-0 p-1.5 list-none bg-bg border-[1.5px] border-border rounded-[14px] max-h-80 overflow-y-auto"
        >
          {options.map((opt, idx) => (
            <li
              key={opt.value}
              role="option"
              aria-selected={opt.value === value}
              data-idx={idx}
              data-focused={idx === focusIdx ? "true" : undefined}
              className="flex items-center justify-between gap-3 px-3 py-2.5 rounded-[10px] text-[15px] text-text cursor-pointer select-none data-[focused=true]:bg-accent-soft data-[focused=true]:text-accent aria-selected:font-semibold"
              onMouseEnter={() => setFocusIdx(idx)}
              onMouseDown={(e) => {
                e.preventDefault();
                select(idx);
              }}
            >
              <span>{opt.label}</span>
              {opt.value === value && (
                <svg
                  className="text-accent flex-shrink-0"
                  width="14"
                  height="14"
                  viewBox="0 0 14 14"
                  aria-hidden="true"
                >
                  <path
                    d="M2.5 7.5l3 3 6-6"
                    stroke="currentColor"
                    strokeWidth="2"
                    fill="none"
                    strokeLinecap="round"
                    strokeLinejoin="round"
                  />
                </svg>
              )}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
