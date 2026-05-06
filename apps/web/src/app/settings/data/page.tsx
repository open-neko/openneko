import { connection } from "next/server";
import { getOrgId } from "@/lib/db";
import { getDataSourceSettings } from "@/lib/data-source-settings";
import DataSourceForm from "./DataSourceForm";

export default async function SettingsDataPage() {
  await connection();
  const initial = await getDataSourceSettings((await getOrgId()));
  return <DataSourceForm initial={initial} />;
}
