"use client";

import { Suspense, useEffect, useState } from "react";
import { useSearchParams } from "next/navigation";
import { DeviceApproval } from "./DeviceApproval";

type PendingDevice = { userCode: string; name: string } | null;

function DeviceApprovalLoader() {
  const code = useSearchParams().get("code") ?? "";
  const [loaded, setLoaded] = useState<{ code: string; request: PendingDevice } | null>(null);

  useEffect(() => {
    if (!code) return;
    let cancelled = false;
    void fetch(`/api/v1/auth/device/approve?code=${encodeURIComponent(code)}`, { cache: "no-store" })
      .then((res) => (res.ok ? res.json() : { request: null }))
      .catch(() => ({ request: null }))
      .then((body: { request?: PendingDevice }) => {
        if (!cancelled) setLoaded({ code, request: body.request ?? null });
      });
    return () => {
      cancelled = true;
    };
  }, [code]);

  if (code && loaded?.code !== code) return null;
  const request = code ? loaded?.request ?? null : null;
  return (
    <DeviceApproval
      key={code}
      initialCode={request?.userCode ?? ""}
      deviceName={request?.name ?? null}
      codeNotFound={Boolean(code) && !request}
    />
  );
}

export default function DevicePage() {
  return (
    <Suspense fallback={null}>
      <DeviceApprovalLoader />
    </Suspense>
  );
}
