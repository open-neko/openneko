import { connection } from "next/server";
import { getOrgId } from "@/lib/db";
import { getInstallPolicyPayload } from "@/lib/install-policy-settings";
import SecurityForm from "./SecurityForm";

export default async function SettingsSecurityPage() {
  await connection();
  const orgId = await getOrgId();
  const payload = await getInstallPolicyPayload(orgId);
  return <SecurityForm initial={payload} />;
}
