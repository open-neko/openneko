"use client";

import { useState } from "react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Field, Input } from "@/components/ui/field";

export type UnconfinedTurnsState = {
  setting: { on: boolean; host?: string; kernel?: string; reason?: string; by?: string; at?: string };
  system: { available: boolean; blocked?: string; host?: string; kernel?: string; reason?: string };
};

const label = "Run agent turns without OS confinement on this system";

export default function UnconfinedTurnsField({ initial }: { initial: UnconfinedTurnsState }) {
  const [state, setState] = useState(initial);
  const [confirm, setConfirm] = useState("");
  const [saving, setSaving] = useState(false);
  const { setting, system } = state;

  async function put(on: boolean) {
    setSaving(true);
    try {
      const res = await fetch("/api/v1/settings/agent/unconfined", {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(on ? { on, confirm } : { on }),
      });
      const body = await res.json();
      if (!res.ok) throw new Error(body.error ?? "The setting was not saved");
      setState(body as UnconfinedTurnsState);
      setConfirm("");
      toast.success(on ? "Agent turns now run without OS confinement." : "Agent turns without OS confinement are off.");
    } catch (e) {
      toast.error(e instanceof Error ? e.message : String(e));
    } finally {
      setSaving(false);
    }
  }

  if (setting.on) {
    return (
      <Field label={label}>
        <div role="alert" className="text-danger text-ui-body-sm" data-testid="unconfined-on">
          On. Agent turns run without OS confinement. The agent can read the files and reach the network of the OpenNeko
          account. {setting.by ? `${setting.by} turned this on at ${setting.at}. ` : ""}
          {setting.reason ? `Reason: ${setting.reason}.` : ""}
        </div>
        <Button type="button" variant="secondary" size="sm" disabled={saving} onClick={() => put(false)}>
          Turn off
        </Button>
      </Field>
    );
  }

  if (!system.available) {
    return (
      <Field label={label} hint={`Off. ${system.blocked ?? ""}`}>
        <span className="text-text3 text-ui-body-sm" data-testid="unconfined-unavailable">
          You can turn this on only on a system that cannot confine agent turns.
        </span>
      </Field>
    );
  }

  return (
    <Field
      label={label}
      htmlFor="agent-unconfined-confirm"
      hint={`Off, so agent turns do not run. ${system.reason ?? ""}`}
    >
      <div className="text-danger text-ui-body-sm" data-testid="unconfined-warning">
        If you turn this on, each agent turn can read the files and reach the network of the OpenNeko account, also the
        secret key and the database login. The audit log records who turned it on. To confirm, type the host name{" "}
        <strong>{system.host}</strong>.
      </div>
      <Input
        id="agent-unconfined-confirm"
        value={confirm}
        spellCheck={false}
        autoCapitalize="none"
        autoCorrect="off"
        autoComplete="off"
        onChange={(e) => setConfirm(e.target.value)}
      />
      <Button
        type="button"
        variant="danger"
        size="sm"
        disabled={saving || !system.host || confirm.trim() !== system.host}
        onClick={() => put(true)}
      >
        Run agent turns without confinement
      </Button>
    </Field>
  );
}
