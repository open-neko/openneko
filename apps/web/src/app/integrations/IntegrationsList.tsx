"use client";

import { useEffect, useState } from "react";
import { useSearchParams } from "next/navigation";
import { toast } from "sonner";
import AppHeader from "@/components/AppHeader";
import { Button } from "@/components/ui/Button";

type Row = {
  pluginId: string;
  pluginName: string;
  providerLabel: string;
  scopes: string[];
  connected: boolean;
  connectedAt: string | null;
};

export default function IntegrationsList({ initial }: { initial: Row[] }) {
  const [rows, setRows] = useState<Row[]>(initial);
  const [busy, setBusy] = useState<string | null>(null);
  const params = useSearchParams();

  useEffect(() => {
    const err = params.get("error");
    if (err) toast.error(err);
    const ok = params.get("connected");
    if (ok) toast.success(`Connected ${ok}`);
  }, [params]);

  async function disconnect(pluginName: string) {
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
      setRows((prev) =>
        prev.map((r) =>
          r.pluginName === pluginName
            ? { ...r, connected: false, connectedAt: null }
            : r,
        ),
      );
      toast.success(`Disconnected ${pluginName}`);
    } catch (e) {
      toast.error(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(null);
    }
  }

  return (
    <div className="root">
      <AppHeader />
      <div className="greet">Integrations.</div>
      <div className="greet-sub mb-6">
        Connect external accounts so OpenNeko can act on your behalf.
        Each operator connects independently — credentials never leave
        your deployment.
      </div>
      {rows.length === 0 ? (
        <p className="text-[14px] text-text3 mt-4">
          No connect-capable plugins installed. Install one with{" "}
          <code>openneko install &lt;name&gt;</code>.
        </p>
      ) : (
        <ul className="flex flex-col gap-3 mt-2">
          {rows.map((row) => (
            <li
              key={row.pluginName}
              className="flex items-center gap-4 p-4 rounded-xl border border-border bg-bg"
            >
              <div className="flex-1 min-w-0">
                <div className="font-semibold text-text">{row.providerLabel}</div>
                <div className="text-[12px] text-text3 truncate">
                  {row.pluginName}
                </div>
                <div className="text-[12px] text-text3 mt-1 truncate">
                  Scopes: {row.scopes.join(", ")}
                </div>
                {row.connected && row.connectedAt && (
                  <div className="text-[12px] text-text2 mt-1">
                    Connected {new Date(row.connectedAt).toLocaleString()}
                  </div>
                )}
              </div>
              {row.connected ? (
                <Button
                  variant="secondary"
                  disabled={busy === row.pluginName}
                  onClick={() => disconnect(row.pluginName)}
                >
                  {busy === row.pluginName ? "…" : "Disconnect"}
                </Button>
              ) : (
                <a
                  href={`/api/integrations/connect/${encodeURIComponent(row.pluginName)}/start`}
                  className="inline-flex"
                >
                  <Button>Connect</Button>
                </a>
              )}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
