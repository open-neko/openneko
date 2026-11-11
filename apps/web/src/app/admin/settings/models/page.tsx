import { connection } from "next/server";
import { getModelSettings } from "@neko/llm/model-routes";
import { AdminDenied } from "@/app/admin/AdminShell";
import { getCurrentActor } from "@/lib/actor";
import { getOrgId } from "@/lib/db";
import ModelsForm from "./ModelsForm";

export default async function SettingsModelsPage() {
  await connection();
  const actor = await getCurrentActor();
  if (actor.role !== "admin") return <AdminDenied />;

  return <ModelsForm initial={await getModelSettings(await getOrgId())} />;
}
