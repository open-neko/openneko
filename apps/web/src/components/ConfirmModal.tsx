"use client";

import { useEffect, useRef } from "react";
import { createRoot, type Root } from "react-dom/client";
import { cn } from "@/lib/cn";

export type ConfirmDialogOptions = {
  title: string;
  description?: string;
  confirmLabel?: string;
  cancelLabel?: string;
  destructive?: boolean;
};

let activeRoot: Root | null = null;
let activeContainer: HTMLDivElement | null = null;

function cleanup() {
  const root = activeRoot;
  const container = activeContainer;
  activeRoot = null;
  activeContainer = null;
  if (root) {
    queueMicrotask(() => {
      try {
        root.unmount();
      } catch {
        // already unmounted
      }
      if (container?.parentElement) {
        container.parentElement.removeChild(container);
      }
    });
  }
}

export function confirmDialog(options: ConfirmDialogOptions): Promise<boolean> {
  if (typeof window === "undefined" || typeof document === "undefined") {
    return Promise.resolve(false);
  }
  cleanup();
  return new Promise<boolean>((resolve) => {
    const container = document.createElement("div");
    document.body.appendChild(container);
    activeContainer = container;
    const root = createRoot(container);
    activeRoot = root;

    const onChoice = (choice: boolean) => {
      cleanup();
      resolve(choice);
    };

    root.render(<ConfirmDialog options={options} onChoice={onChoice} />);
  });
}

function ConfirmDialog({
  options,
  onChoice,
}: {
  options: ConfirmDialogOptions;
  onChoice: (choice: boolean) => void;
}) {
  const cancelRef = useRef<HTMLButtonElement>(null);

  useEffect(() => {
    cancelRef.current?.focus();
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.preventDefault();
        onChoice(false);
      } else if (e.key === "Enter") {
        e.preventDefault();
        onChoice(true);
      }
    };
    window.addEventListener("keydown", onKey);
    const prevOverflow = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    return () => {
      window.removeEventListener("keydown", onKey);
      document.body.style.overflow = prevOverflow;
    };
  }, [onChoice]);

  const onBackdrop = (e: React.MouseEvent<HTMLDivElement>) => {
    if (e.target === e.currentTarget) onChoice(false);
  };

  return (
    <div
      className="fixed inset-0 z-[1000] bg-[rgba(20,18,12,0.45)] backdrop-blur-[4px] flex items-center justify-center p-4 animate-[modal-fade_0.15s_ease-out]"
      onClick={onBackdrop}
      role="dialog"
      aria-modal="true"
      aria-labelledby="confirm-modal-title"
    >
      <div className="w-full max-w-[420px] bg-card border border-border rounded-[18px] px-[22px] pt-[22px] pb-[18px] shadow-[0_8px_32px_rgba(20,18,12,0.18),0_24px_60px_rgba(20,18,12,0.10)] animate-[modal-rise_0.18s_cubic-bezier(0.16,1,0.3,1)]">
        <h2
          id="confirm-modal-title"
          className="font-display text-base font-bold leading-tight text-text m-0"
        >
          {options.title}
        </h2>
        {options.description ? (
          <p className="mt-2 text-[13.5px] leading-[1.55] text-text2 whitespace-pre-line">
            {options.description}
          </p>
        ) : null}
        <div className="mt-[18px] flex justify-end gap-2">
          <button
            ref={cancelRef}
            type="button"
            onClick={() => onChoice(false)}
            className="text-[13px] font-medium px-3.5 py-2 rounded-[10px] border border-border bg-card text-text cursor-pointer transition-all duration-150 hover:bg-black/5 hover:border-text3 focus-visible:outline-2 focus-visible:outline-accent focus-visible:outline-offset-2"
          >
            {options.cancelLabel ?? "Cancel"}
          </button>
          <button
            type="button"
            onClick={() => onChoice(true)}
            className={cn(
              "text-[13px] font-medium px-3.5 py-2 rounded-[10px] border cursor-pointer transition-all duration-150 focus-visible:outline-2 focus-visible:outline-accent focus-visible:outline-offset-2",
              !options.destructive &&
                "bg-text border-text text-bg hover:bg-[#1a1814] hover:border-[#1a1814]",
              options.destructive &&
                "bg-[#c0392b] border-[#c0392b] text-white hover:bg-[#a83224] hover:border-[#a83224]",
            )}
          >
            {options.confirmLabel ?? "Confirm"}
          </button>
        </div>
      </div>
    </div>
  );
}
