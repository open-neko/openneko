"use client";

import { useEffect, useState } from "react";
import { useSearchParams } from "next/navigation";
import { toast } from "sonner";
import AppHeader from "@/components/AppHeader";
import PageHeading from "@/components/PageHeading";
import { Button, ButtonLink } from "@/components/ui/Button";

type Row = {
  pluginId: string;
  pluginName: string;
  providerLabel: string;
  scopes: string[];
  flow: string;
  credentialScope: string;
  connected: boolean;
  connectedAt: string | null;
};

type InitialState = { workspace: Row[]; connectors: Row[] };

export default function IntegrationsList({ initial }: { initial: InitialState }) {
  const [workspace, setWorkspace] = useState<Row[]>(initial.workspace);
  const [connectors, setConnectors] = useState<Row[]>(initial.connectors);
  const [busy, setBusy] = useState<string | null>(null);
  const params = useSearchParams();

  useEffect(() => {
    const err = params.get("error");
    if (err) toast.error(err);
    const ok = params.get("connected");
    if (ok) toast.success(`Connected ${ok}`);
  }, [params]);

  async function disconnect(pluginName: string, isDeployment: boolean) {
    setBusy(pluginName);
    try {
      const res = await fetch(
        `/api/integrations/disconnect/${encodeURIComponent(pluginName)}`,
        { method: "POST" },
      );
      if (!res.ok) {
        const body = (await res.json().catch(() => null)) as { error?: string } | null;
        throw new Error(body?.error ?? `HTTP ${res.status}`);
      }
      const update = (prev: Row[]) =>
        prev.map((r) =>
          r.pluginName === pluginName
            ? { ...r, connected: false, connectedAt: null }
            : r,
        );
      if (isDeployment) setWorkspace(update);
      else setConnectors(update);
      toast.success(`Disconnected ${pluginName}`);
    } catch (e) {
      toast.error(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(null);
    }
  }

  function RowView({
    row,
    isDeployment,
  }: {
    row: Row;
    isDeployment: boolean;
  }) {
    return (
      <li
        key={row.pluginName}
        className="flex items-center gap-4 p-4 rounded-xl border border-border bg-bg max-[480px]:items-stretch max-[480px]:flex-col"
      >
        <div className="flex-1 min-w-0">
          <div className="font-display text-ui-subsection font-bold text-text">{row.providerLabel}</div>
          <div className="text-ui-caption text-text3 truncate">
            {row.pluginName}
          </div>
          <div className="text-ui-caption text-text3 mt-1 truncate">
            Scopes: {row.scopes.join(", ")}
          </div>
          {row.connected && row.connectedAt && (
            <div className="text-ui-caption text-text2 mt-1">
              Connected {new Date(row.connectedAt).toLocaleString()}
            </div>
          )}
        </div>
        {row.connected ? (
          <Button
            variant="secondary"
            disabled={busy === row.pluginName}
            onClick={() => disconnect(row.pluginName, isDeployment)}
          >
            {busy === row.pluginName ? "…" : "Disconnect"}
          </Button>
        ) : (
          <ButtonLink
            href={`/api/integrations/connect/${encodeURIComponent(row.pluginName)}/start`}
          >
            Connect
          </ButtonLink>
        )}
      </li>
    );
  }

  return (
    <div className="root">
      <AppHeader />
      <PageHeading
        eyebrow="Workspace connections"
        title="Integrations"
        description="Connect external accounts for agent actions. Credentials remain in this deployment."
      />

      {workspace.length > 0 && (
        <>
          <h2 className="mt-6 mb-2 font-display text-ui-section font-bold text-text">
            Workspace connections
          </h2>
          <p className="text-ui-body-sm text-text3 mb-2">
            One org-wide authorization, consented once by an admin and shared
            by every operator. Authorizing opens a browser consent screen that
            names the scopes and the endpoint.
          </p>
          <ul className="flex flex-col gap-3">
            {workspace.map((row) => (
              <RowView key={row.pluginName} row={row} isDeployment />
            ))}
          </ul>
        </>
      )}

      {connectors.length > 0 && (
        <>
          <h2 className="mt-6 mb-2 font-display text-ui-section font-bold text-text">
            Per-operator connectors
          </h2>
          <p className="text-ui-body-sm text-text3 mb-2">
            Each operator authorizes independently with their own account.
          </p>
          <ul className="flex flex-col gap-3">
            {connectors.map((row) => (
              <RowView key={row.pluginName} row={row} isDeployment={false} />
            ))}
          </ul>
        </>
      )}

      {workspace.length === 0 && connectors.length === 0 && (
        <p className="text-ui-body text-text3 mt-4">
          No connect-capable plugins installed. Install one with{" "}
          <code>openneko install &lt;name&gt;</code>.
        </p>
      )}
    </div>
  );
}
