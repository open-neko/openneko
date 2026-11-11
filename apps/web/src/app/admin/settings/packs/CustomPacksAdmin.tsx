"use client";

import { useCallback, useEffect, useRef, useState, type FormEvent } from "react";
import { toast } from "sonner";
import { confirmDialog } from "@/components/ConfirmModal";
import PackSetup, { installBuiltinPack, packApi } from "@/components/admin/PackSetup";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Disclosure } from "@/components/ui/disclosure";
import { Field, Input, NativeSelect } from "@/components/ui/field";
import { Badge } from "@/components/ui/badge";

type CatalogPack = { source: "embedded" | "uploaded"; id: string; name: string; version: string; installed: boolean; status: string; lastError: string | null };

export default function CustomPacksAdmin({ initialPack = "", connected = "" }: { initialPack?: string; connected?: string }) {
  const [catalog, setCatalog] = useState<CatalogPack[] | null>(null);
  const [selected, setSelected] = useState("");
  const [opened, setOpened] = useState(0);
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState("");
  const [errorDetail, setErrorDetail] = useState("");
  const dirty = useRef(false);
  const fileInput = useRef<HTMLInputElement>(null);
  const initialLoadStarted = useRef(false);
  const errorText = useRef<HTMLParagraphElement>(null);

  const fail = useCallback((error: unknown, message: string) => {
    setError(message);
    setErrorDetail(error instanceof Error ? error.message : String(error));
    requestAnimationFrame(() => errorText.current?.focus());
  }, []);
  const refresh = useCallback(async () => {
    try {
      const result = await packApi<{ packs: CatalogPack[] }>("/api/v1/admin/packs");
      setCatalog(result.packs);
    } catch (error) { fail(error, "The pack list could not be loaded. Try again."); }
  }, [fail]);
  useEffect(() => { const timer = window.setTimeout(() => void refresh(), 0); return () => clearTimeout(timer); }, [refresh]);

  const open = useCallback(async (id: string) => {
    const trigger = document.activeElement as HTMLElement | null;
    if (dirty.current && !await confirmDialog({ title: "Discard pack changes?", description: "The current configuration has not been applied.", confirmLabel: "Discard changes" })) { trigger?.focus(); return false; }
    dirty.current = false;
    setError("");
    setSelected(id);
    setOpened(count => count + 1);
    return true;
  }, []);
  useEffect(() => {
    if (!initialPack || initialLoadStarted.current) return;
    initialLoadStarted.current = true;
    const timer = window.setTimeout(() => {
      void open(initialPack).then(() => {
        if (connected) toast.success("Pack account connected.");
        window.history.replaceState({}, "", window.location.pathname);
      });
    }, 0);
    return () => window.clearTimeout(timer);
  }, [connected, initialPack, open]);

  async function upload(event: FormEvent) {
    event.preventDefault();
    const file = fileInput.current?.files?.[0];
    if (!file || file.size === 0 || file.size > 16 * 1024 * 1024) {
      setError("Choose a non-empty ZIP pack of at most 16 MiB."); setErrorDetail(""); fileInput.current?.focus(); return;
    }
    setBusy("upload"); setError("");
    try {
      const response = await fetch("/api/v1/admin/packs/upload", { method: "POST", headers: { "Content-Type": "application/zip" }, body: file });
      const result = await response.json();
      if (!response.ok) throw new Error(result.error ?? "Upload failed");
      await refresh();
      toast.success("Pack uploaded for review. Nothing has been installed.");
      if (fileInput.current) fileInput.current.value = "";
      await open(result.packId);
    } catch (error) { fail(error, "The pack could not be uploaded. Check the ZIP archive and try again."); }
    finally { setBusy(null); }
  }

  async function installBuiltin(pack: CatalogPack) {
    setBusy(`install-${pack.id}`); setError("");
    try {
      await installBuiltinPack(pack.id, pack.version);
      toast.success(`${pack.name} installed. Configure its connections when ready.`);
      await refresh();
      await open(pack.id);
    } catch (error) {
      if (await open(pack.id)) fail(error, "The pack needs configuration before it can be installed. Complete the fields below and try again.");
    } finally { setBusy(null); }
  }

  return <section className="grid gap-4 mb-6" aria-label="Solution packs" aria-busy={busy !== null}>
    <Card as="section" className="grid gap-4">
      <div className="grid gap-1"><h2>Built-in packs</h2><p className="text-ui-body-sm text-text2">These packs are included with OpenNeko. Install them here, then configure their connections.</p></div>
      {catalog === null ? (!error && <p role="status">Loading packs…</p>) : <ul className="grid gap-3">{catalog.filter(pack => pack.source === "embedded").map(pack => <li key={pack.id} className="flex flex-wrap items-center justify-between gap-3 border-b border-border pb-3 last:border-b-0 last:pb-0">
        <div className="grid gap-1"><div className="flex flex-wrap items-center gap-2"><h3 className="font-display text-ui-subsection font-bold">{pack.name}</h3><Badge variant={pack.installed ? "success" : "muted"}>{pack.installed ? "Installed" : "Available"}</Badge></div><p className="text-ui-caption text-text2">Version {pack.version}</p></div>
        <Button variant="secondary" disabled={busy !== null} onClick={() => void (pack.installed ? open(pack.id) : installBuiltin(pack))}>{busy === `install-${pack.id}` ? "Installing…" : pack.installed ? "Manage" : "Install"}</Button>
      </li>)}</ul>}
      <Disclosure title="Upload a custom pack">
        <form onSubmit={upload} className="grid gap-3 sm:grid-cols-[1fr_auto] sm:items-end">
          <Field label="Pack archive" htmlFor="pack-archive"><Input id="pack-archive" name="archive" ref={fileInput} type="file" accept=".zip,application/zip" aria-describedby="pack-archive-hint" disabled={busy !== null} /></Field>
          <Button type="submit" variant="secondary" disabled={busy !== null}>{busy === "upload" ? "Uploading…" : "Upload pack"}</Button>
          <p id="pack-archive-hint" className="text-ui-caption text-text3 sm:col-span-2">ZIP format, up to 16 MiB. Uploading does not install the pack.</p>
        </form>
      </Disclosure>
        {catalog?.some(pack => pack.source === "uploaded") ? <Field label="Uploaded pack" htmlFor="custom-pack"><NativeSelect id="custom-pack" value={selected} disabled={busy !== null} onChange={event => { if (event.target.value) void open(event.target.value); }}><option value="">Choose a pack</option>{catalog.filter(pack => pack.source === "uploaded").map(pack => <option key={pack.id} value={pack.id}>{pack.name} · {pack.version}</option>)}</NativeSelect></Field> : null}
      {error ? <p ref={errorText} tabIndex={-1} role="alert" className="break-words text-ui-body-sm text-danger">{error}</p> : null}
      {error && errorDetail ? <Disclosure title="Error details"><p className="break-words text-ui-body-sm">{errorDetail}</p></Disclosure> : null}
      {error && catalog === null ? <Button variant="secondary" onClick={() => { setError(""); void refresh(); }} disabled={busy !== null}>Retry loading packs</Button> : null}
    </Card>
    {selected ? <PackSetup key={`${selected}:${opened}`} packId={selected} onChanged={refresh} onDirtyChange={value => { dirty.current = value; }} /> : null}
  </section>;
}
