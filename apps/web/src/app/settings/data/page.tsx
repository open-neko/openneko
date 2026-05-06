import { getOrgId } from "@/lib/db";
import { getDataSourceSettings } from "@/lib/data-source-settings";
import DataSourceForm from "./DataSourceForm";

export default async function SettingsDataPage() {
  const initial = await getDataSourceSettings((await getOrgId()));
  return <DataSourceForm initial={initial} />;
}
