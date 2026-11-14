"use client";

import AdminOnly from "@/app/admin/AdminOnly";
import { AdminShell } from "@/app/admin/AdminShell";
import ChannelSettingsPanel from "./ChannelSettingsPanel";

export default function SettingsChannelsPage() {
  return (
    <AdminOnly>
      <AdminShell
        title="Channels"
        subtitle="Slack, Telegram, and other places where people talk to OpenNeko. A channel turns on when its settings are saved."
        back={{ href: "/admin/settings", label: "Settings" }}
      >
        <ChannelSettingsPanel />
      </AdminShell>
    </AdminOnly>
  );
}
