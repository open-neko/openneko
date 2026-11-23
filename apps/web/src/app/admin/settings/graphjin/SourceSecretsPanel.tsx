"use client";

import { useState } from "react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Field, Input } from "@/components/ui/field";

type SourceSecretSummary = {
  name: string;
  description: string | null;
  host?: string | null;
  port?: number | null;
  dbname?: string | null;
  login?: string | null;
  allowUnverifiedTls?: boolean;
  updatedAt: string;
};

export default function SourceSecretsPanel({
  initialSecrets,
}: {
  initialSecrets: SourceSecretSummary[];
}) {
  const [secrets, setSecrets] = useState(initialSecrets);
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [value, setValue] = useState("");
  const [host, setHost] = useState("");
  const [port, setPort] = useState("");
  const [dbname, setDbname] = useState("");
  const [login, setLogin] = useState("");
  const [allowUnverified, setAllowUnverified] = useState(false);
  const [saving, setSaving] = useState(false);

  async function refresh() {
    const res = await fetch("/api/v1/settings/source-secrets");
    if (!res.ok) throw new Error(`HTTP ${res.status}`);
    const body = (await res.json()) as { secrets?: SourceSecretSummary[] };
    setSecrets(body.secrets ?? []);
  }

  async function saveSecret() {
    setSaving(true);
    try {
      const trimmedName = name.trim();
      const res = await fetch("/api/v1/settings/source-secrets", {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          name: trimmedName,
          value,
          description: description.trim() || null,
          host: host.trim(),
          port: port.trim(),
          dbname: dbname.trim(),
          user: login.trim(),
          allowUnverifiedTls: allowUnverified,
        }),
      });
      const body = (await res.json().catch(() => null)) as
        | { error?: string }
        | null;
      if (!res.ok) throw new Error(body?.error ?? `HTTP ${res.status}`);
      await refresh();
      setName("");
      setDbname("");
      setLogin("");
      setDescription("");
      setValue("");
      setHost("");
      setPort("");
      setAllowUnverified(false);
      toast.success(`Saved ${trimmedName}.`);
    } catch (e) {
      toast.error(e instanceof Error ? e.message : String(e));
    } finally {
      setSaving(false);
    }
  }

  async function deleteSecret(secretName: string) {
    setSaving(true);
    try {
      const res = await fetch(
        `/api/v1/settings/source-secrets?name=${encodeURIComponent(secretName)}`,
        { method: "DELETE" },
      );
      const body = (await res.json().catch(() => null)) as
        | { error?: string }
        | null;
      if (!res.ok) throw new Error(body?.error ?? `HTTP ${res.status}`);
      setSecrets((prev) => prev.filter((secret) => secret.name !== secretName));
      toast.success(`Removed ${secretName}.`);
    } catch (e) {
      toast.error(e instanceof Error ? e.message : String(e));
    } finally {
      setSaving(false);
    }
  }

  return (
    <section className="settings-card">
      <div className="settings-card-head">
        <div>
          <h2 className="settings-card-title">Connection secrets</h2>
          <p className="settings-card-copy">
            Named credentials for GraphJin source registration requests. OpenNeko sends a password only to the host and port saved with it, with the database and login saved with it.
          </p>
        </div>
      </div>

      <div className="grid gap-3 md:grid-cols-3">
        <Field label="Name" htmlFor="source-secret-name">
          <Input
            id="source-secret-name"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="WAREHOUSE_DB_PASSWORD"
            spellCheck={false}
            autoComplete="off"
          />
        </Field>
        <Field label="Description" htmlFor="source-secret-description">
          <Input
            id="source-secret-description"
            value={description}
            onChange={(e) => setDescription(e.target.value)}
            placeholder="Warehouse read user"
          />
        </Field>
        <Field label="Value" htmlFor="source-secret-value">
          <Input
            id="source-secret-value"
            type="password"
            value={value}
            onChange={(e) => setValue(e.target.value)}
            placeholder="Stored encrypted"
            autoComplete="off"
            spellCheck={false}
          />
        </Field>
        <Field label="Host" htmlFor="source-secret-host">
          <Input
            id="source-secret-host"
            value={host}
            onChange={(e) => setHost(e.target.value)}
            placeholder="db.internal"
            spellCheck={false}
            autoComplete="off"
          />
        </Field>
        <Field label="Port" htmlFor="source-secret-port">
          <Input
            id="source-secret-port"
            inputMode="numeric"
            value={port}
            onChange={(e) => setPort(e.target.value)}
            placeholder="5432"
            autoComplete="off"
          />
        </Field>
        <Field label="Database" htmlFor="source-secret-dbname">
          <Input
            id="source-secret-dbname"
            value={dbname}
            onChange={(e) => setDbname(e.target.value)}
            placeholder="warehouse"
            spellCheck={false}
            autoComplete="off"
          />
        </Field>
        <Field label="Login" htmlFor="source-secret-login">
          <Input
            id="source-secret-login"
            value={login}
            onChange={(e) => setLogin(e.target.value)}
            placeholder="openneko_read"
            spellCheck={false}
            autoComplete="off"
          />
        </Field>
        <div className="flex items-end">
          <Checkbox
            label="Allow a server certificate that does not verify"
            checked={allowUnverified}
            onCheckedChange={(checked) => setAllowUnverified(checked === true)}
          />
        </div>
        <div className="flex items-end">
          <Button
            type="button"
            variant="secondary"
            disabled={saving || !name.trim() || !value || !host.trim() || !port.trim() || !dbname.trim() || !login.trim()}
            onClick={saveSecret}
          >
            Save
          </Button>
        </div>
      </div>

      <div className="mt-5 flex flex-col gap-2">
        {secrets.length === 0 ? (
          <p className="text-sm text-text2">No connection secrets stored.</p>
        ) : (
          secrets.map((secret) => (
            <div
              key={secret.name}
              className="flex items-center gap-3 rounded-lg border border-border bg-bg px-3 py-2 text-sm"
            >
              <code className="min-w-0 flex-1 truncate text-text">
                {secret.name}
              </code>
              <span className="hidden min-w-0 flex-1 truncate text-text3 md:block">
                {secret.description ?? "No description"}
              </span>
              <span className="min-w-0 truncate text-text3">
                {secret.host && secret.dbname && secret.login ? `${secret.host}:${secret.port} · ${secret.dbname} · ${secret.login}` : "No host, database or login: save again"}
              </span>
              <span className="text-xs text-text3">
                {formatDate(secret.updatedAt)}
              </span>
              <Button
                type="button"
                variant="danger"
                size="sm"
                disabled={saving}
                onClick={() => void deleteSecret(secret.name)}
              >
                Remove
              </Button>
            </div>
          ))
        )}
      </div>
    </section>
  );
}

function formatDate(value: string): string {
  return new Date(value).toLocaleDateString("en-US", {
    year: "numeric",
    month: "short",
    day: "2-digit",
  });
}
