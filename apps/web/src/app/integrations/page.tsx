import { getCurrentUser, getAuthProvider } from "@/lib/auth";
import { getOrgId } from "@neko/db";
import { listPackUserConnections } from "@neko/llm/graphjin/pack-user-connections";
import { personalConnectionsFixture } from "./visual-fixture";
import { connection } from "next/server";
import { redirect } from "next/navigation";
import { getCurrentActor } from "@/lib/actor";
import {
  getDeploymentConnectStatus,
  getOperatorConnectStatus,
  listConnectProviders,
} from "@/lib/integrations";
import IntegrationsList from "./IntegrationsList";

export default async function IntegrationsPage({ searchParams }: { searchParams: Promise<{ state?: string }> }) {
  await connection();
  const query = await searchParams;
  if (process.env.NODE_ENV !== "production" && process.env.OPENNEKO_RECORDS_VISUAL_TEST === "true" && ["connected", "unconfigured", "disconnected"].includes(query.state ?? "")) return <IntegrationsList key={`preview-${query.state}`} preview initial={{ workspace: [], connectors: [], personal: await personalConnectionsFixture(query.state!) }} isAdmin={false} />;
  const actor = await getCurrentActor();
  const user = await getCurrentUser();
  if (!user && await getAuthProvider()) {
    redirect("/signin?returnTo=/integrations");
  }
  const [providers, status, deploymentStatus] = await Promise.all([
    actor.role === "admin" ? listConnectProviders() : Promise.resolve([]),
    actor.userId ? getOperatorConnectStatus(actor.userId) : Promise.resolve([]),
    actor.role === "admin" ? getDeploymentConnectStatus() : Promise.resolve([]),
  ]);
  const connectedByPlugin = new Map(status.map((s) => [s.pluginName, s]));
  const deploymentConnectedByPlugin = new Map(
    deploymentStatus.map((s) => [s.pluginName, s]),
  );
  const rowFor = (
    p: (typeof providers)[number],
    map: Map<string, { connectedAt: string; scopes?: string[] }>,
  ) => ({
    pluginId: p.pluginId,
    pluginName: p.pluginName,
    providerLabel: p.providerLabel,
    scopes: p.scopes,
    flow: p.flow,
    credentialScope: p.credentialScope,
    connected: map.has(p.pluginName),
    connectedAt: map.get(p.pluginName)?.connectedAt ?? null,
  });
  const workspace = providers
    .filter((p) => p.credentialScope === "deployment")
    .map((p) => rowFor(p, deploymentConnectedByPlugin));
  const connectors = providers
    .filter((p) => p.credentialScope !== "deployment")
    .map((p) => rowFor(p, connectedByPlugin));
  const personal = user ? await listPackUserConnections({ orgId: await getOrgId(), userId: user.id }) : [];
  return <IntegrationsList key="live" initial={{ workspace, connectors, personal }} isAdmin={actor.role === "admin"} />;
}
