/**
 * Read helpers for connectors (the `connect` capability). The worker holds
 * each credential; openneko web runs the OAuth flow. The `pluginName` and
 * `pluginId` wire fields keep their old names; their values are connector
 * ids.
 */

import "server-only";
import { createHash, randomBytes } from "node:crypto";

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

function workerAdminBase(): string {
  const raw = process.env.WORKER_ADMIN_URL ?? "http://127.0.0.1:4100";
  return raw.replace(/\/+$/, "");
}

export async function listConnectProviders(): Promise<ConnectProvider[]> {
  try {
    const res = await fetch(`${workerAdminBase()}/admin/connect/providers`, {
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
    const res = await fetch(
      `${workerAdminBase()}/admin/connect/status/${encodeURIComponent(operatorId)}`,
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
    const res = await fetch(
      `${workerAdminBase()}/admin/connect/deployment/status`,
      { cache: "no-store", signal: AbortSignal.timeout(2000) },
    );
    if (!res.ok) return [];
    const body = (await res.json()) as { connected?: ConnectStatus[] };
    return body.connected ?? [];
  } catch {
    return [];
  }
}

export function newStateToken(): string {
  return randomBytes(24).toString("base64url");
}

export function newPkceVerifier(): string {
  // RFC 7636: 43-128 chars, [A-Z / a-z / 0-9 / "-" / "." / "_" / "~"].
  // 32 random bytes → 43-char base64url is the most common shape.
  return randomBytes(32).toString("base64url");
}

export function pkceChallenge(verifier: string): string {
  return createHash("sha256").update(verifier).digest("base64url");
}
