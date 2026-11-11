"use client";

import { useState } from "react";
import { useRouter } from "next/navigation";
import type { DeviceRow } from "@neko/db";
import { AdminError } from "@/components/admin/AdminError";
import { adminApi } from "@/components/admin/admin-api";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";

const STATUS_LABEL: Record<DeviceRow["status"], string> = {
  active: "Signed in",
  idle: "Expired after 30 unused days",
  revoked: "Revoked",
};

export function DevicesPanel({ devices }: { devices: DeviceRow[] }) {
  const router = useRouter();
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const active = devices.filter((d) => d.status === "active").length;

  async function revoke(device: DeviceRow) {
    if (!window.confirm(`Revoke ${device.name}? ${device.userEmail} must sign in on it again.`)) return;
    setBusy(device.id);
    setError(null);
    const result = await adminApi(`/api/v1/admin/devices/${device.id}`, "DELETE");
    setBusy(null);
    if (!result.ok) return setError(result.error);
    router.refresh();
  }

  return (
    <>
      <div className="settings-card-head">
        <div>
          <h2 className="settings-card-title">Devices</h2>
          <p className="settings-card-copy">
            The installed app signs in with a code that the person approves in a signed-in browser. Revoke a device to sign it out at once.
          </p>
        </div>
        <div className="settings-source">
          <strong className="is-ok">{active} signed in</strong>
        </div>
      </div>
      <AdminError message={error} />
      {devices.length === 0 ? (
        <p className="text-sm text-text2">No device has signed in yet.</p>
      ) : (
        <div className="overflow-x-auto">
          <Table className="w-full border-collapse text-left text-sm">
            <TableHeader>
              <TableRow>
                <TableHead className="border-b border-border px-3 py-2 font-bold">Device</TableHead>
                <TableHead className="border-b border-border px-3 py-2 font-bold">Person</TableHead>
                <TableHead className="border-b border-border px-3 py-2 font-bold">Last used</TableHead>
                <TableHead className="border-b border-border px-3 py-2 font-bold">Status</TableHead>
                <TableHead className="border-b border-border px-3 py-2 font-bold"><span className="sr-only">Action</span></TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {devices.map((device) => (
                <TableRow key={device.id} className="border-b border-border last:border-0">
                  <TableCell className="px-3 py-3">
                    <div className="font-semibold text-text">{device.name}</div>
                    <div className="font-mono text-xs text-text3">{device.tokenPrefix}</div>
                  </TableCell>
                  <TableCell className="px-3 py-3 text-text2">{device.userName ?? device.userEmail}</TableCell>
                  <TableCell className="whitespace-nowrap px-3 py-3 text-text2">{formatDate(device.lastUsedAt)}</TableCell>
                  <TableCell className="px-3 py-3">
                    <Badge variant={device.status === "active" ? "secondary" : "muted"}>{STATUS_LABEL[device.status]}</Badge>
                  </TableCell>
                  <TableCell className="px-3 py-3 text-right">
                    {device.status === "active" ? (
                      <Button size="sm" disabled={busy === device.id} onClick={() => void revoke(device)}>
                        {busy === device.id ? "Revoking…" : "Revoke"}
                      </Button>
                    ) : null}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      )}
    </>
  );
}

function formatDate(value: string): string {
  return new Date(value).toLocaleString("en-US", {
    year: "numeric",
    month: "short",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
  });
}
