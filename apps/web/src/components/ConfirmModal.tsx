"use client";

import { useEffect, useRef } from "react";
import { createRoot, type Root } from "react-dom/client";

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
      className="modal-backdrop"
      onClick={onBackdrop}
      role="dialog"
      aria-modal="true"
      aria-labelledby="confirm-modal-title"
    >
      <div className="modal-card">
        <h2 id="confirm-modal-title" className="modal-title">
          {options.title}
        </h2>
        {options.description ? (
          <p className="modal-desc">{options.description}</p>
        ) : null}
        <div className="modal-actions">
          <button
            ref={cancelRef}
            type="button"
            onClick={() => onChoice(false)}
            className="modal-btn"
          >
            {options.cancelLabel ?? "Cancel"}
          </button>
          <button
            type="button"
            onClick={() => onChoice(true)}
            className={`modal-btn is-primary${options.destructive ? " is-destructive" : ""}`}
          >
            {options.confirmLabel ?? "Confirm"}
          </button>
        </div>
      </div>
    </div>
  );
}
