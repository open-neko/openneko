/**
 * Read helpers for connectors (the `connect` capability). The worker holds
 * each credential; openneko web runs the OAuth flow. The `pluginName` and
 * `pluginId` wire fields keep their old names; their values are connector
 * ids.
 */

import "server-only";
import { workerAdminFetch } from "@neko/llm/worker-admin";

export interface ConnectProvider {
  pluginId: string;
  pluginName: string;
  providerLabel: string;
  scopes: string[];
  flow: string;
  credentialScope: string;
}

export interface ConnectStatus {
  pluginName: string;
  connectedAt: string;
  scopes?: string[];
}

export async function listConnectProviders(): Promise<ConnectProvider[]> {
  try {
    const res = await workerAdminFetch(`/admin/connect/providers`, {
      cache: "no-store",
      signal: AbortSignal.timeout(2000),
    });
    if (!res.ok) return [];
    const body = (await res.json()) as { providers?: ConnectProvider[] };
    return body.providers ?? [];
  } catch {
    return [];
  }
}

export async function getOperatorConnectStatus(
  operatorId: string,
): Promise<ConnectStatus[]> {
  try {
    const res = await workerAdminFetch(
      `/admin/connect/status/${encodeURIComponent(operatorId)}`,
      { cache: "no-store", signal: AbortSignal.timeout(2000) },
    );
    if (!res.ok) return [];
    const body = (await res.json()) as { connected?: ConnectStatus[] };
    return body.connected ?? [];
  } catch {
    return [];
  }
}

export async function getDeploymentConnectStatus(): Promise<ConnectStatus[]> {
  try {
    const res = await workerAdminFetch(
      `/admin/connect/deployment/status`,
      { cache: "no-store", signal: AbortSignal.timeout(2000) },
    );
    if (!res.ok) return [];
    const body = (await res.json()) as { connected?: ConnectStatus[] };
    return body.connected ?? [];
  } catch {
    return [];
  }
}
