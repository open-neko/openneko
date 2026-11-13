"use client";

import { useState } from "react";
import {
  AlertDialog,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { copySkill, suggestedCopyName } from "@/lib/skill-actions";

export function SkillCopyDialog({
  source,
  onClose,
  onCopied,
}: {
  source: string;
  onClose: () => void;
  onCopied: (name: string) => void;
}) {
  const [name, setName] = useState(suggestedCopyName(source));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const submit = async () => {
    setBusy(true);
    setError(null);
    try {
      const copy = await copySkill(source, name.trim());
      onCopied(copy.name);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "The skill could not be copied.");
    } finally {
      setBusy(false);
    }
  };

  return (
    <AlertDialog open onOpenChange={(open) => !open && onClose()}>
      <AlertDialogContent className="max-w-[420px] rounded-card border border-border bg-card px-[22px] pb-[18px] pt-[22px] shadow-lift">
        <form
          onSubmit={(event) => {
            event.preventDefault();
            void submit();
          }}
        >
          <AlertDialogHeader>
            <AlertDialogTitle className="font-display text-base font-bold leading-tight text-text">
              Copy {source}
            </AlertDialogTitle>
            <AlertDialogDescription className="mt-2 text-left text-ui-body leading-[1.55] text-text2">
              The copy is yours. Only you see it, and you can edit it. {source} stays as it is.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <label className="mt-4 block text-ui-caption font-semibold text-text2" htmlFor="skill-copy-name">
            Name of the copy
          </label>
          <Input
            id="skill-copy-name"
            className="mt-1 w-full"
            value={name}
            maxLength={64}
            autoFocus
            onChange={(event) => setName(event.target.value)}
          />
          {error ? (
            <p className="mt-2 text-ui-caption text-danger" role="alert">
              {error}
            </p>
          ) : null}
          <AlertDialogFooter className="mx-0 mb-0 mt-[18px] border-0 bg-transparent p-0">
            <AlertDialogCancel size="sm" type="button" onClick={onClose}>
              Cancel
            </AlertDialogCancel>
            <Button variant="primary" size="sm" type="submit" disabled={busy || name.trim() === ""}>
              {busy ? "Copying…" : "Copy skill"}
            </Button>
          </AlertDialogFooter>
        </form>
      </AlertDialogContent>
    </AlertDialog>
  );
}
