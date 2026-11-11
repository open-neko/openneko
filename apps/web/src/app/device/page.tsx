import { connection } from "next/server";
import { findDeviceRequest } from "@neko/db";
import { getOrgId } from "@/lib/db";
import { DeviceApproval } from "./DeviceApproval";

export default async function DevicePage({ searchParams }: { searchParams: Promise<{ code?: string }> }) {
  await connection();
  const { code } = await searchParams;
  const request = code ? await findDeviceRequest(await getOrgId(), code) : null;
  return (
    <DeviceApproval
      initialCode={request?.userCode ?? ""}
      deviceName={request?.name ?? null}
      codeNotFound={Boolean(code) && !request}
    />
  );
}
