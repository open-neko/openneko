import { connection } from "next/server";
import { AdminDenied, AdminShell } from "@/app/admin/AdminShell";
import { getCurrentActor } from "@/lib/actor";
import ChannelSettingsPanel from "./ChannelSettingsPanel";

export default async function SettingsChannelsPage() {
  await connection();
  const actor = await getCurrentActor();
  if (actor.role !== "admin") return <AdminDenied />;

  return (
    <AdminShell
      title="Channels"
      subtitle="Slack, Telegram, and other places where people talk to OpenNeko. A channel turns on when its settings are saved."
      back={{ href: "/admin/settings", label: "Settings" }}
    >
      <ChannelSettingsPanel />
    </AdminShell>
  );
}
