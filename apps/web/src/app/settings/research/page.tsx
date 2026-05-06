import { connection } from "next/server";
import { getOrgId } from "@/lib/db";
import { getProviderSettingsPayload } from "@/lib/provider-settings";
import ResearchForm from "./ResearchForm";

export default async function SettingsResearchPage() {
  await connection();
  const providers = await getProviderSettingsPayload((await getOrgId()));
  return <ResearchForm initial={providers} />;
}
