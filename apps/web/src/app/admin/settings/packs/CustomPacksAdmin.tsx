"use client";

import { useCallback, useEffect, useRef, useState, type FormEvent } from "react";
import { toast } from "sonner";
import { confirmDialog } from "@/components/ConfirmModal";
import { ActionGroup } from "@/components/ui/ActionGroup";
import { Button } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { Checkbox } from "@/components/ui/Checkbox";
import { Disclosure } from "@/components/ui/Disclosure";
import { EmptyState } from "@/components/ui/EmptyState";
import { Field, Input, NativeSelect } from "@/components/ui/Field";
import { LocalDateTime } from "@/components/ui/LocalDateTime";
import { Pill } from "@/components/ui/Pill";

type Value = string | number | boolean;
type Operation = "install" | "configure" | "upgrade";
type CatalogPack = { id: string; name: string; version: string; installed: boolean };
type Source = { id: string; name: string; label: string | null; enabled: boolean; graphqlUrl: string };
type Configuration = { inputs: Record<string, Value>; dataSourceId?: string; sourceBindings: Record<string, string> };
type Status = { version: string; status: string; lastError: string | null; installedAt: string | null; configuration?: Configuration };
type Inspection = {
  source: string; bundleHash: string;
  manifest: {
    metadata: { id: string; name: string; version: string; publisher: string };
    inputs: Array<{ key: string; type: string; required?: boolean; default?: Value; description?: string; values?: Value[] }>;
    secrets: Array<{ key: string; required?: boolean }>;
    artifacts: { graphjin?: unknown };
  };
  bindingRequirements: Array<{ key: string; name: string }>;
  permissions: Record<string, string>;
};
type Review = Inspection & { reviewHash: string; inputs: Record<string, Value>; runtime: { source?: { id: string }; bindings: Record<string, string> }; plan: { entries: Array<{ action: string; kind: string; key: string; targetRef: string; reason?: string }> } };

async function api<T>(path: string, body?: unknown): Promise<T> {
  const response = await fetch(path, body === undefined ? { cache: "no-store" } : { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
  const result = await response.json();
  if (!response.ok) throw new Error(result.error ?? `Request failed (${response.status})`);
  return result as T;
}

function label(key: string): string {
  if (key === "apiWrite") return "API changes";
  if (key === "database") return "Database access";
  const words = key.replaceAll(/[._-]/g, " ");
  return words.charAt(0).toUpperCase() + words.slice(1);
}

export default function CustomPacksAdmin() {
  const [catalog, setCatalog] = useState<CatalogPack[] | null>(null);
  const [sources, setSources] = useState<Source[]>([]);
  const [selected, setSelected] = useState("");
  const [detail, setDetail] = useState<Inspection | null>(null);
  const [status, setStatus] = useState<Status | null>(null);
  const [operation, setOperation] = useState<Operation>("install");
  const [inputs, setInputs] = useState<Record<string, Value>>({});
  const [secrets, setSecrets] = useState<Record<string, string>>({});
  const [bindings, setBindings] = useState<Record<string, string>>({});
  const [sourceId, setSourceId] = useState("");
  const [review, setReview] = useState<{ result: Review; request: Record<string, unknown> } | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState("");
  const [errorDetail, setErrorDetail] = useState("");
  const [dirty, setDirty] = useState(false);
  const fileInput = useRef<HTMLInputElement>(null);
  const removeButton = useRef<HTMLButtonElement>(null);
  const errorText = useRef<HTMLParagraphElement>(null);

  function fail(error: unknown, message = "The pack could not be loaded. Try loading it again.") {
    setError(message);
    setErrorDetail(error instanceof Error ? error.message : String(error));
    requestAnimationFrame(() => errorText.current?.focus());
  }
  function changed() { setReview(null); setDirty(true); setError(""); }
  const refresh = useCallback(async () => {
    try {
      const result = await api<{ packs: CatalogPack[] }>("/api/admin/packs");
      setCatalog(result.packs.filter(pack => pack.id !== "magento"));
    } catch (error) { fail(error); }
  }, []);
  useEffect(() => { const timer = window.setTimeout(() => void refresh(), 0); return () => clearTimeout(timer); }, [refresh]);
  useEffect(() => {
    if (!dirty) return;
    const warn = (event: BeforeUnloadEvent) => { event.preventDefault(); event.returnValue = ""; };
    window.addEventListener("beforeunload", warn);
    return () => window.removeEventListener("beforeunload", warn);
  }, [dirty]);

  async function loadPack(id: string, mode?: Operation) {
    const trigger = document.activeElement as HTMLElement | null;
    if (dirty && !await confirmDialog({ title: "Discard pack changes?", description: "The current configuration has not been applied.", confirmLabel: "Discard changes" })) { trigger?.focus(); return; }
    setBusy("loading"); setError(""); setReview(null); setDetail(null); setDirty(false); setSecrets({}); setSelected(id);
    try {
      const path = `/api/admin/packs/${encodeURIComponent(id)}`;
      const response = await fetch(`${path}/status`, { cache: "no-store" });
      const current = response.status === 404 ? null : await response.json() as Status;
      if (!response.ok && response.status !== 404) throw new Error("Could not load pack status");
      const action = mode ?? (current?.status === "installed" ? "configure" : "install");
      const inspection = await api<Inspection>(`${path}/inspect${action === "configure" && current ? `?version=${encodeURIComponent(current.version)}` : ""}`);
      const available = inspection.manifest.artifacts.graphjin
        ? await api<{ sources: Source[] }>("/api/settings/data-sources")
        : { sources: [] };
      setSources(available.sources.filter(source => source.enabled && source.graphqlUrl));
      setDetail(inspection); setStatus(current); setOperation(action);
      setInputs(Object.fromEntries(inspection.manifest.inputs.map(input => [input.key, current?.configuration?.inputs[input.key] ?? input.default ?? (input.type === "boolean" ? false : "")])));
      setBindings(current?.configuration?.sourceBindings ?? {});
      setSourceId(current?.configuration?.dataSourceId ?? "");
    } catch (error) { fail(error); }
    finally { setBusy(null); }
  }

  async function upload(event: FormEvent) {
    event.preventDefault();
    const file = fileInput.current?.files?.[0];
    if (!file || file.size === 0 || file.size > 16 * 1024 * 1024) {
      setError("Choose a non-empty ZIP pack of at most 16 MiB."); setErrorDetail(""); fileInput.current?.focus(); return;
    }
    setBusy("upload"); setError("");
    try {
      const response = await fetch("/api/admin/packs/upload", { method: "POST", headers: { "Content-Type": "application/zip" }, body: file });
      const result = await response.json();
      if (!response.ok) throw new Error(result.error ?? "Upload failed");
      await refresh();
      toast.success("Pack uploaded for review. Nothing has been installed.");
      if (fileInput.current) fileInput.current.value = "";
      await loadPack(result.packId);
    } catch (error) { fail(error, "The pack could not be uploaded. Check the ZIP archive and try again."); }
    finally { setBusy(null); }
  }

  async function prepare(event: FormEvent) {
    event.preventDefault();
    if (!detail) return;
    setBusy("review"); setReview(null); setError("");
    const request = { operation, version: detail.manifest.metadata.version, inputs: Object.fromEntries(Object.entries(inputs).filter(([, value]) => value !== "")), secrets: Object.fromEntries(Object.entries(secrets).filter(([, value]) => value !== "")), sourceBindings: bindings, ...(sourceId ? { dataSourceId: sourceId } : {}) };
    try {
      const result = await api<Review>(`/api/admin/packs/${selected}/review`, request);
      setReview({ result, request });
    } catch (error) { fail(error, "The pack could not be reviewed. Check the configuration and try again."); }
    finally { setBusy(null); }
  }

  async function apply() {
    if (!review) return;
    setBusy(operation); setError("");
    try {
      const result = await api<Status>(`/api/admin/packs/${selected}/${operation}`, { ...review.request, reviewHash: review.result.reviewHash, idempotencyKey: crypto.randomUUID() });
      setStatus(result); setReview(null); setSecrets({}); setDirty(false);
      toast.success("Pack changes applied."); await refresh();
      setOperation("configure");
    } catch (error) { setReview(null); fail(error, "The pack could not be applied. Check the connection and credentials, then review again."); }
    finally { setBusy(null); }
  }

  async function remove() {
    if (!await confirmDialog({ title: "Remove this pack?", description: "Its automations and connector access will stop. History and business data are retained.", confirmLabel: "Remove pack", destructive: true })) { removeButton.current?.focus(); return; }
    setBusy("uninstall"); setError("");
    try {
      setStatus(await api<Status>(`/api/admin/packs/${selected}/uninstall`, { idempotencyKey: crypto.randomUUID() }));
      setReview(null); setSecrets({}); setDirty(false); setOperation("install");
      toast.success("Pack removed. History and data retained."); await refresh();
    } catch (error) { fail(error, "The pack could not be removed. Check its status and try again."); }
    finally { setBusy(null); }
  }

  return <section className="grid gap-4 mb-6" aria-label="Custom packs" aria-busy={busy !== null}>
    <Card as="section" className="grid gap-4">
      <div><h2>Custom packs</h2><p className="text-ui-body-sm text-text2">Upload a pack, review its configuration and changes, then choose when to install it.</p></div>
      <form onSubmit={upload} className="grid gap-3 sm:grid-cols-[1fr_auto] sm:items-end">
        <Field label="Pack archive" htmlFor="pack-archive" hint="ZIP format, up to 16 MiB. Uploading does not install the pack.">
          <Input id="pack-archive" name="archive" ref={fileInput} type="file" accept=".zip,application/zip" disabled={busy !== null} />
        </Field>
        <Button type="submit" variant="primary" disabled={busy !== null}>{busy === "upload" ? "Uploading…" : "Upload pack"}</Button>
      </form>
      {error ? <p ref={errorText} tabIndex={-1} role="alert" className="break-words text-ui-body-sm text-danger">{error}</p> : null}
      {error && errorDetail ? <Disclosure title="Error details"><p className="break-words text-ui-body-sm">{errorDetail}</p></Disclosure> : null}
      {catalog === null ? <p role="status">Loading packs…</p> : catalog.length === 0 ? <EmptyState title="No custom packs yet" body="Choose a pack archive above to review your first pack." className="py-4" /> :
        <Field label="Available pack" htmlFor="custom-pack"><NativeSelect id="custom-pack" value={selected} disabled={busy !== null} onChange={event => { if (event.target.value) void loadPack(event.target.value); }}>
          <option value="">Choose a pack</option>
          {catalog.map(pack => <option key={pack.id} value={pack.id}>{pack.name} · {pack.version}{pack.installed ? " · Installed" : ""}</option>)}
        </NativeSelect></Field>}
      {error && !detail ? <Button variant="secondary" onClick={() => { setError(""); void (selected ? loadPack(selected) : refresh()); }} disabled={busy !== null}>Retry loading packs</Button> : null}
      {busy === "loading" ? <p role="status">Loading pack configuration…</p> : null}
    </Card>
    {detail ? <Card as="section" className="grid gap-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div><h2>{detail.manifest.metadata.name}</h2><p className="text-ui-body-sm text-text2">Version {detail.manifest.metadata.version} · Published by {detail.manifest.metadata.publisher}</p>
          {status?.status === "installed" && status.installedAt ? <p className="text-ui-caption text-text2">Installed <LocalDateTime value={status.installedAt} /></p> : null}</div>
        <Pill variant={status?.status === "installed" ? "success" : "muted"}>{status?.status === "installed" ? "Installed" : "Not installed"}</Pill>
      </div>
      {status?.lastError ? <Disclosure title="Last operation failed"><p className="break-words text-ui-body-sm">{status.lastError}</p></Disclosure> : null}
      {status?.status === "installed" ? <ActionGroup align="start">
        <Button variant="secondary" disabled={busy !== null} onClick={() => void loadPack(selected, operation === "upgrade" ? "configure" : "upgrade")}>{operation === "upgrade" ? "Edit installed version" : "Review available update"}</Button>
        <Button ref={removeButton} variant="danger" disabled={busy !== null} onClick={() => void remove()}>{busy === "uninstall" ? "Removing…" : "Remove pack"}</Button>
      </ActionGroup> : null}
      <form onSubmit={prepare} className="grid gap-4">
        <fieldset disabled={busy !== null} className="grid gap-4 sm:grid-cols-2">
          <legend className="mb-3 font-display text-ui-subsection font-bold">{operation === "upgrade" ? "Update configuration" : "Pack configuration"}</legend>
          {detail.manifest.artifacts.graphjin ? <Field label="Data connection" htmlFor="pack-source" hint="Choose an enabled connection from Data settings.">
            <NativeSelect id="pack-source" required value={sourceId} onChange={event => { changed(); setSourceId(event.target.value); }}>
              <option value="">Choose a connection</option>
              {sources.map(source => <option key={source.id} value={source.id}>{source.label || source.name || source.id}</option>)}
            </NativeSelect>
          </Field> : null}
          {detail.manifest.inputs.map(input => input.type === "boolean" ? <Checkbox key={input.key} label={label(input.key)} checked={Boolean(inputs[input.key])} onChange={event => { changed(); setInputs({ ...inputs, [input.key]: event.target.checked }); }} /> :
            <Field key={input.key} label={label(input.key)} hint={input.description} htmlFor={`pack-${input.key}`}>
              {input.type === "enum" ? <NativeSelect id={`pack-${input.key}`} value={String(inputs[input.key] ?? "")} required={input.required} onChange={event => { changed(); setInputs({ ...inputs, [input.key]: input.values?.find(value => String(value) === event.target.value) ?? "" }); }}>
                <option value="">Choose a value</option>{input.values?.map(value => <option key={String(value)} value={String(value)}>{String(value)}</option>)}
              </NativeSelect> : <Input id={`pack-${input.key}`} name={input.key} type={input.type === "url" ? "url" : input.type === "integer" ? "number" : "text"} step={input.type === "integer" ? 1 : undefined} required={input.required} value={String(inputs[input.key] ?? "")} onChange={event => { changed(); setInputs({ ...inputs, [input.key]: input.type === "integer" && event.target.value !== "" ? Number(event.target.value) : event.target.value }); }} />}
            </Field>)}
          {(detail.bindingRequirements ?? []).map(binding => <Field key={binding.key} label={`${label(binding.name)} source`} htmlFor={`binding-${binding.key}`} hint="Existing read-only source name in this connection."><Input id={`binding-${binding.key}`} required value={bindings[binding.key] ?? ""} onChange={event => { changed(); setBindings({ ...bindings, [binding.key]: event.target.value }); }} /></Field>)}
          {detail.manifest.secrets.map(secret => <Field key={secret.key} label={label(secret.key)} htmlFor={`secret-${secret.key}`} hint={status?.status === "installed" ? "Leave blank to keep the saved credential." : "Stored securely after installation."}>
            <Input id={`secret-${secret.key}`} name={secret.key} type="password" autoComplete="new-password" spellCheck={false} required={secret.required !== false && status?.status !== "installed"} value={secrets[secret.key] ?? ""} onChange={event => { changed(); setSecrets({ ...secrets, [secret.key]: event.target.value }); }} />
          </Field>)}
        </fieldset>
        <ActionGroup align="start"><Button type="submit" variant={review ? "secondary" : "primary"} disabled={busy !== null}>{busy === "review" ? "Reviewing…" : "Review changes"}</Button></ActionGroup>
      </form>
      {review ? <section className="grid gap-3" aria-label="Reviewed changes" aria-live="polite">
        <h3>Review before applying</h3>
        <p className="text-ui-body-sm">{review.result.manifest.metadata.name} · Version {review.result.manifest.metadata.version}. Applying these changes enables the pack&apos;s configured automations and reads.</p>
        <p className="text-ui-body-sm">{review.result.runtime.source ? `Connection: ${sources.find(source => source.id === review.result.runtime.source?.id)?.label || sources.find(source => source.id === review.result.runtime.source?.id)?.name || "Selected data connection"}.` : "No data connection is required."} {review.result.plan.entries.filter(entry => entry.action === "create").length} additions, {review.result.plan.entries.filter(entry => entry.action === "update").length} updates, {review.result.plan.entries.filter(entry => entry.action === "retire").length} removals.</p>
        <ul className="grid gap-1 text-ui-body-sm">{Object.entries(review.result.permissions).map(([key, value]) => <li key={key}>{label(key)}: {value}</li>)}</ul>
        <Disclosure title="Configuration and change details"><pre className="overflow-x-auto whitespace-pre-wrap break-all text-ui-caption">{JSON.stringify({ inputs: review.result.inputs, sources: review.result.runtime.bindings, changes: review.result.plan.entries.map(({ action, kind, targetRef, reason }) => ({ action, kind, target: targetRef, reason })), content: review.result.bundleHash }, null, 2)}</pre></Disclosure>
        <Button variant="primary" disabled={busy !== null || review.result.plan.entries.some(entry => entry.action === "conflict")} onClick={() => void apply()}>{busy === operation ? "Applying…" : operation === "install" ? "Approve and install" : "Approve and apply changes"}</Button>
        {review.result.plan.entries.some(entry => entry.action === "conflict") ? <p role="alert">Resolve the conflicting changes, then review again.</p> : null}
      </section> : null}
    </Card> : null}
  </section>;
}
