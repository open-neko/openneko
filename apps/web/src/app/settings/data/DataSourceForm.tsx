"use client";

import { useState } from "react";
import AppHeader from "@/components/AppHeader";
import { toast } from "sonner";

type DataSourcePayload = {
  source: "org" | "unset";
  kind: string;
  graphqlUrl: string;
  mcpUrl: string;
  label: string;
};

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
      <div className="greet-sub" style={{ marginBottom: 24 }}>Graphjin server endpoint OpenNeko should connect to.</div>

      <section className="settings-card">
        <div className="settings-field-stack">
          <label className="settings-field">
            <span className="settings-label">GraphJin URL *</span>
            <input
              className="settings-input"
              value={data.rootUrl}
              placeholder="http://localhost:8080"
              onChange={(e) => setData((p) => ({ ...p, rootUrl: e.target.value }))}
            />
            <span className="settings-help">
              Just the base URL — OpenNeko handles the GraphQL and MCP endpoints automatically.
            </span>
          </label>
          <label className="settings-field">
            <span className="settings-label">Label</span>
            <input
              className="settings-input"
              value={data.label}
              placeholder="primary"
              onChange={(e) => setData((p) => ({ ...p, label: e.target.value }))}
            />
          </label>
        </div>
        <div className="settings-actions">
          <button type="button" className="pill" onClick={test} disabled={testing || !data.rootUrl.trim()}>
            {testing ? "Testing…" : "Test connection"}
          </button>
          <button type="button" className="pill on" onClick={save} disabled={saving || !data.rootUrl.trim()}>
            {saving ? "Saving…" : "Save settings"}
          </button>
        </div>
      </section>
    </div>
  );
}
