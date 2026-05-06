import { connection } from "next/server";
import { getOrgId } from "@/lib/db";
import { getAgentSettingsPayload } from "@/lib/agent-backend-settings";
import { getProviderSettingsPayload } from "@/lib/provider-settings";
import AgentForm from "./AgentForm";

export default async function SettingsAgentPage() {
  await connection();
  const [agent, providers] = await Promise.all([
    getAgentSettingsPayload((await getOrgId())),
    getProviderSettingsPayload((await getOrgId())),
  ]);
  return <AgentForm initial={{ agent, providers }} />;
}
