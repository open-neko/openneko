"use client";

import { useEffect, useState } from "react";
import type { PackUserConnectionStatus } from "@neko/llm/graphjin/pack-user-connections";
import { useSession } from "@/lib/session-client";

export type IntegrationRow = {
  pluginId: string;
  pluginName: string;
  providerLabel: string;
  scopes: string[];
  flow: string;
  credentialScope: string;
  connected: boolean;
  connectedAt: string | null;
};

export type LoadedIntegrations = {
  role: "admin" | "member" | null;
  workspace: IntegrationRow[];
  connectors: IntegrationRow[];
  personal: PackUserConnectionStatus[];
};

export function useIntegrationsData(): LoadedIntegrations | null {
  const session = useSession();
  const [data, setData] = useState<{
    workspace: IntegrationRow[];
    connectors: IntegrationRow[];
    personal: PackUserConnectionStatus[];
  } | null>(null);

  useEffect(() => {
    if (!session) return;
    if (session.authEnabled && !session.user) {
      window.location.assign("/signin?returnTo=/integrations");
      return;
    }
    let cancelled = false;
    const isAdmin = session.role === "admin";
    void Promise.all([
      isAdmin
        ? fetch("/api/v1/integrations/list", { cache: "no-store" })
            .then((res) => (res.ok ? res.json() : { workspace: [], connectors: [] }))
            .catch(() => ({ workspace: [], connectors: [] }))
        : Promise.resolve({ workspace: [] as IntegrationRow[], connectors: [] as IntegrationRow[] }),
      session.user
        ? fetch("/api/v1/my/pack-accounts", { cache: "no-store" })
            .then((res) => (res.ok ? res.json() : { connections: [] }))
            .catch(() => ({ connections: [] }))
        : Promise.resolve({ connections: [] as PackUserConnectionStatus[] }),
    ]).then(([listBody, personalBody]: [
      { workspace?: IntegrationRow[]; connectors?: IntegrationRow[] },
      { connections?: PackUserConnectionStatus[] },
    ]) => {
      if (!cancelled) {
        setData({
          workspace: listBody.workspace ?? [],
          connectors: listBody.connectors ?? [],
          personal: personalBody.connections ?? [],
        });
      }
    });
    return () => {
      cancelled = true;
    };
  }, [session]);

  if (!session || !data) return null;
  return {
    role: session.role,
    workspace: data.workspace,
    connectors: data.connectors,
    personal: data.personal,
  };
}
