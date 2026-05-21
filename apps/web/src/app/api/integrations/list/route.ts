import { NextResponse } from "next/server";
import { getCurrentUser } from "@/lib/auth";
import {
  getOperatorConnectStatus,
  listConnectProviders,
} from "@/lib/integrations";

/**
 * GET /api/integrations/list — combined view of installed connect
 * plugins + per-operator connection status. Used by the /integrations
 * page to render the table in one round-trip.
 */
export async function GET() {
  const user = await getCurrentUser();
  if (!user) {
    return NextResponse.json({ error: "not signed in" }, { status: 401 });
  }
  const [providers, status] = await Promise.all([
    listConnectProviders(),
    getOperatorConnectStatus(user.id),
  ]);
  const connectedByPlugin = new Map(status.map((s) => [s.pluginName, s]));
  const rows = providers.map((p) => ({
    pluginId: p.pluginId,
    pluginName: p.pluginName,
    providerLabel: p.providerLabel,
    scopes: p.scopes,
    connected: connectedByPlugin.has(p.pluginName),
    connectedAt: connectedByPlugin.get(p.pluginName)?.connectedAt ?? null,
  }));
  return NextResponse.json({ providers: rows });
}
