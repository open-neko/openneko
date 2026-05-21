import { connection } from "next/server";
import { redirect } from "next/navigation";
import { getCurrentUser } from "@/lib/auth";
import {
  getOperatorConnectStatus,
  listConnectProviders,
} from "@/lib/integrations";
import IntegrationsList from "./IntegrationsList";

export default async function IntegrationsPage() {
  await connection();
  const user = await getCurrentUser();
  if (!user) {
    redirect("/signin?returnTo=/integrations");
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
  return <IntegrationsList initial={rows} />;
}
