"use client";

import { useState } from "react";
import { toast } from "sonner";
import { ActionGroup } from "@/components/ui/action-group";
import { Button } from "@/components/ui/button";

export type PackChoice = { policyId: string; packId: string; question: string; line: string };

export default function PackChoices({ choices, onDecided }: { choices: PackChoice[]; onDecided: () => void }) {
  if (choices.length === 0) return null;
  return (
    <section className="settings-card mt-6 grid gap-4" aria-labelledby="pack-choices-title">
      <h2 id="pack-choices-title" className="settings-card-title">A pack rule needs your choice</h2>
      <ul className="m-0 grid list-none gap-4 p-0">
        {choices.map((choice) => (
          <ChoiceRow key={choice.policyId} choice={choice} onDecided={onDecided} />
        ))}
      </ul>
    </section>
  );
}

function ChoiceRow({ choice, onDecided }: { choice: PackChoice; onDecided: () => void }) {
  const [busy, setBusy] = useState<"ask" | "auto" | null>(null);
  const [error, setError] = useState<string | null>(null);

  async function decide(mode: "ask" | "auto") {
    setBusy(mode);
    setError(null);
    try {
      const response = await fetch(`/api/v1/policies/${encodeURIComponent(choice.policyId)}/pack-choice`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ mode }),
      });
      const body = await response.json().catch(() => ({}));
      if (!response.ok) throw new Error(body.error ?? "Could not save the choice.");
      toast.success(mode === "ask" ? "A person approves each search." : "Searches run with no approval.");
      onDecided();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Could not save the choice.");
    } finally {
      setBusy(null);
    }
  }

  return (
    <li className="grid gap-2">
      <p className="m-0 font-semibold text-text">{choice.question}</p>
      <p className="m-0 text-ui-body-sm text-text2">{choice.line}</p>
      {error ? <p role="alert" className="m-0 text-ui-body-sm text-danger">{error}</p> : null}
      <ActionGroup align="start">
        <Button size="sm" variant="primary" disabled={busy !== null} onClick={() => void decide("ask")}>
          {busy === "ask" ? "Saving…" : "Ask before each search"}
        </Button>
        <Button size="sm" variant="ghost" disabled={busy !== null} onClick={() => void decide("auto")}>
          {busy === "auto" ? "Saving…" : "Keep auto"}
        </Button>
      </ActionGroup>
    </li>
  );
}
