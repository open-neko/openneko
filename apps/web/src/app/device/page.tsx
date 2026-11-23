"use client";

import { Suspense, useEffect, useState } from "react";
import { useSearchParams } from "next/navigation";
import { useSession } from "@/lib/session-client";
import { DeviceApproval } from "./DeviceApproval";

type PendingDevice = { userCode: string; name: string } | null;

export function deviceSignInUrl(code: string): string {
  const returnTo = code ? `/device?code=${encodeURIComponent(code)}` : "/device";
  return `/signin?returnTo=${encodeURIComponent(returnTo)}`;
}

function DeviceApprovalLoader() {
  const code = useSearchParams().get("code") ?? "";
  const session = useSession();
  const [loaded, setLoaded] = useState<{ code: string; request: PendingDevice } | null>(null);

  useEffect(() => {
    if (session === undefined) return;
    if (!session || (session.authEnabled && !session.user)) {
      window.location.assign(deviceSignInUrl(code));
      return;
    }
    if (!code) return;
    let cancelled = false;
    void fetch(`/api/v1/auth/device/approve?code=${encodeURIComponent(code)}`, { cache: "no-store" })
      .then(async (res) => {
        if (res.status === 401) {
          if (!cancelled) window.location.assign(deviceSignInUrl(code));
          return null;
        }
        return (res.ok ? await res.json() : { request: null }) as { request?: PendingDevice };
      })
      .catch(() => ({ request: null }))
      .then((body) => {
        if (!cancelled && body) setLoaded({ code, request: body.request ?? null });
      });
    return () => {
      cancelled = true;
    };
  }, [code, session]);

  if (!session || (session.authEnabled && !session.user)) return null;
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
