"use client";

import { useState } from "react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Field } from "@/components/ui/field";
import { NativeSelect } from "@/components/ui/native-select";

type Workflow = { id: string; name: string; apiAccess?: boolean; webhook?: boolean };
type User = { id: string; email: string; name: string | null };

const called = (workflow: Workflow) => Boolean(workflow.apiAccess || workflow.webhook);

export default function OwnerlessWorkflows({ workflows, users }: { workflows: Workflow[]; users: User[] }) {
  const [waiting, setWaiting] = useState(() => [...workflows].sort((a, b) => Number(called(b)) - Number(called(a))));
  if (waiting.length === 0) return null;
  return (
    <section className="settings-card mb-6 grid gap-4" aria-labelledby="ownerless-title">
      <div>
        <h2 id="ownerless-title" className="settings-card-title">Workflows with no owner</h2>
        <p className="settings-card-copy">
          These workflows do not run until you assign an owner. Each one then runs with its owner&apos;s access.
        </p>
        {waiting.some(called) ? (
          <p className="settings-card-copy">
            Before 6.0.0, a workflow with API access or a webhook ran as the service account. Its callers now get a 409 answer. Assign an owner and the API key and the webhook keep working.
          </p>
        ) : null}
      </div>
      <ul className="grid gap-3">
        {waiting.map((workflow) => (
          <OwnerRow
            key={workflow.id}
            workflow={workflow}
            users={users}
            onAssigned={() => setWaiting((list) => list.filter((item) => item.id !== workflow.id))}
          />
        ))}
      </ul>
    </section>
  );
}

function OwnerRow({ workflow, users, onAssigned }: { workflow: Workflow; users: User[]; onAssigned: () => void }) {
  const [owner, setOwner] = useState("");
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function assign(event: React.FormEvent) {
    event.preventDefault();
    setSaving(true);
    setError(null);
    try {
      const response = await fetch(`/api/v1/workflows/${encodeURIComponent(workflow.id)}`, {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ ownerUserId: owner }),
      });
      const body = await response.json().catch(() => ({}));
      if (!response.ok) throw new Error(body.error ?? "Could not assign the owner.");
      toast.success(`${workflow.name} has an owner.`);
      onAssigned();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Could not assign the owner.");
    } finally {
      setSaving(false);
    }
  }

  const selectId = `owner-${workflow.id}`;
  return (
    <li>
      <form onSubmit={assign} className="grid gap-2 sm:grid-cols-[minmax(0,1fr)_auto] sm:items-end">
        <Field label={workflow.name} htmlFor={selectId} error={error ?? undefined}
          hint={called(workflow) ? [workflow.apiAccess ? "API access" : null, workflow.webhook ? "webhook" : null].filter(Boolean).join(" · ") + ": callers wait for an owner" : undefined}>
          <NativeSelect id={selectId} value={owner} onChange={(event) => setOwner(event.target.value)}>
            <option value="">Choose an owner</option>
            {users.map((user) => (
              <option key={user.id} value={user.id}>{user.name ? `${user.name} (${user.email})` : user.email}</option>
            ))}
          </NativeSelect>
        </Field>
        <Button type="submit" variant="secondary" disabled={!owner || saving}>{saving ? "Assigning…" : "Assign"}</Button>
      </form>
    </li>
  );
}
