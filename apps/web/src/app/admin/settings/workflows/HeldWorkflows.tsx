"use client";

import Link from "next/link";
import { useState } from "react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";

type Workflow = { id: string; name: string; versionHash: string };

export default function HeldWorkflows({ workflows }: { workflows: Workflow[] }) {
  const [waiting, setWaiting] = useState(workflows);
  if (waiting.length === 0) return null;
  return (
    <section className="settings-card mb-6 grid gap-4" aria-labelledby="held-title">
      <div>
        <h2 id="held-title" className="settings-card-title">Changed workflows with API access</h2>
        <p className="settings-card-copy">
          A run changed these workflows, or a skill they use, after you gave them API access. API and webhook runs wait until you read the new version and approve it. When the workflow changes again after you opened this page, the approval is refused; reload and read it again.
        </p>
      </div>
      <ul className="grid gap-3">
        {waiting.map((workflow) => (
          <HeldRow
            key={workflow.id}
            workflow={workflow}
            onApproved={() => setWaiting((list) => list.filter((item) => item.id !== workflow.id))}
          />
        ))}
      </ul>
    </section>
  );
}

function HeldRow({ workflow, onApproved }: { workflow: Workflow; onApproved: () => void }) {
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function approve() {
    setSaving(true);
    setError(null);
    try {
      const response = await fetch(`/api/v1/workflows/${encodeURIComponent(workflow.id)}`, {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ approveChanges: workflow.versionHash }),
      });
      const body = await response.json().catch(() => ({}));
      if (!response.ok) throw new Error(body.error ?? "Could not approve the workflow.");
      toast.success(`${workflow.name} runs from the API again.`);
      onApproved();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Could not approve the workflow.");
    } finally {
      setSaving(false);
    }
  }

  return (
    <li className="grid gap-2 sm:grid-cols-[minmax(0,1fr)_auto] sm:items-center">
      <div className="min-w-0">
        <Link href={`/workflows?id=${encodeURIComponent(workflow.id)}`} className="font-medium underline">
          {workflow.name}
        </Link>
        {error ? <p role="alert" className="text-ui-body-sm text-danger">{error}</p> : null}
      </div>
      <Button type="button" variant="secondary" disabled={saving} onClick={() => void approve()}>
        {saving ? "Approving…" : "Approve"}
      </Button>
    </li>
  );
}
