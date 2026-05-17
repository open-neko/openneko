"use client";

import { useState } from "react";
import AppHeader from "@/components/AppHeader";
import { toast } from "sonner";
import { Button } from "@/components/ui/Button";

type DataSourcePayload = {
  source: "org" | "unset";
  kind: string;
  graphqlUrl: string;
  mcpUrl: string;
  label: string;
};

const INPUT_CLS =
  "px-[13px] py-[11px] sm:px-3.5 sm:py-[13px] rounded-xl border-[1.5px] border-border bg-bg text-text text-base sm:text-[15px] font-body outline-none transition-all duration-200 focus:border-accent focus:shadow-[0_0_0_3px_rgba(107,92,231,0.08)]";
const FIELD_CLS = "flex flex-col gap-2";
const LABEL_CLS = "text-[14px] font-semibold text-text";
const HELP_CLS = "text-[13px] text-text3 leading-[1.45]";

const GRAPHQL_SUFFIX = "/api/v1/graphql";
const MCP_SUFFIX = "/api/v1/mcp";

function deriveEndpoints(rootUrl: string): { graphqlUrl: string; mcpUrl: string } {
  const root = deriveRoot(rootUrl);
  return { graphqlUrl: `${root}${GRAPHQL_SUFFIX}`, mcpUrl: `${root}${MCP_SUFFIX}` };
}

// Accept whatever the user pastes — bare root, trailing slash, or a full
// GraphJin endpoint URL — and reduce it to a clean root so deriveEndpoints
// can append the canonical suffixes without doubling them.
function deriveRoot(input: string): string {
  let s = input.trim().replace(/\/+$/, "");
  const lower = s.toLowerCase();
  if (lower.endsWith(GRAPHQL_SUFFIX)) s = s.slice(0, -GRAPHQL_SUFFIX.length);
  else if (lower.endsWith(MCP_SUFFIX)) s = s.slice(0, -MCP_SUFFIX.length);
  return s.replace(/\/+$/, "");
}

export default function DataSourceForm({ initial }: { initial: DataSourcePayload }) {
  const [data, setData] = useState({
    rootUrl: deriveRoot(initial.graphqlUrl),
    label: initial.label || "primary",
  });
  const [saving, setSaving] = useState(false);
  const [testing, setTesting] = useState(false);

  async function test() {
    setTesting(true);
    try {
      const { graphqlUrl, mcpUrl } = deriveEndpoints(data.rootUrl);
      const res = await fetch("/api/settings/data-source/test", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ graphqlUrl, mcpUrl }),
      });
      const body = await res.json();
      if (!res.ok) throw new Error(body.error ?? "Test failed");
      toast.success(
        body.mcpOk === false ? "GraphQL reachable. MCP unreachable." : "Connection looks good.",
      );
    } catch (e) {
      toast.error(e instanceof Error ? e.message : String(e));
    } finally {
      setTesting(false);
    }
  }

  async function save() {
    setSaving(true);
    try {
      const { graphqlUrl, mcpUrl } = deriveEndpoints(data.rootUrl);
      const res = await fetch("/api/settings/data-source", {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ graphqlUrl, mcpUrl, label: data.label }),
      });
      const body = await res.json();
      if (!res.ok) throw new Error(body.error ?? "Save failed");
      toast.success("Data source saved.");
    } catch (e) {
      toast.error(e instanceof Error ? e.message : String(e));
    } finally {
      setSaving(false);
    }
  }

  return (
    <div className="root">
      <AppHeader back={{ href: "/settings", label: "All settings" }} />
      <div className="greet">Data source.</div>
      <div className="greet-sub mb-6">Graphjin server endpoint OpenNeko should connect to.</div>

      <section className="settings-card">
        <div className="grid gap-4 mt-4">
          <label className={FIELD_CLS}>
            <span className={LABEL_CLS}>GraphJin URL *</span>
            <input
              className={INPUT_CLS}
              value={data.rootUrl}
              placeholder="http://localhost:8080"
              onChange={(e) => setData((p) => ({ ...p, rootUrl: e.target.value }))}
            />
            <span className={HELP_CLS}>
              Just the base URL — OpenNeko handles the GraphQL and MCP endpoints automatically.
            </span>
          </label>
          <label className={FIELD_CLS}>
            <span className={LABEL_CLS}>Label</span>
            <input
              className={INPUT_CLS}
              value={data.label}
              placeholder="primary"
              onChange={(e) => setData((p) => ({ ...p, label: e.target.value }))}
            />
          </label>
        </div>
        <div className="flex justify-end gap-2.5 mt-5 max-[720px]:flex-col max-[720px]:items-stretch [&>button]:max-[720px]:w-full">
          <Button onClick={test} disabled={testing || !data.rootUrl.trim()}>
            {testing ? "Testing…" : "Test connection"}
          </Button>
          <Button variant="primary" onClick={save} disabled={saving || !data.rootUrl.trim()}>
            {saving ? "Saving…" : "Save settings"}
          </Button>
        </div>
      </section>
    </div>
  );
}
