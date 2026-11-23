"use client";

import { useEffect, useState } from "react";
import { useRouter } from "next/navigation";
import AdminOnly from "@/app/admin/AdminOnly";
import { MAGENTO_VISUAL_FIXTURE } from "../../magento-visual-fixture";
import MagentoPackAdmin from "../../MagentoPackAdmin";

function magentoVisualFixtureEnabled(): boolean {
  return (
    process.env.NODE_ENV !== "production" &&
    (process.env.OPENNEKO_MAGENTO_VISUAL_TEST === "true" ||
      process.env.NEXT_PUBLIC_VISUAL_TEST === "true")
  );
}

function MagentoOperationsGate() {
  const router = useRouter();
  const [ready, setReady] = useState(false);

  useEffect(() => {
    let cancelled = false;
    void fetch("/api/v1/admin/packs/magento/status", { cache: "no-store" })
      .then(async (res) => {
        if (!res.ok) return null;
        return (await res.json()) as { status?: string; configuration?: { required?: boolean } };
      })
      .catch(() => null)
      .then((status) => {
        if (cancelled) return;
        if (!status || status.status !== "installed" || status.configuration?.required) {
          router.replace("/admin/settings/packs?pack=magento");
          return;
        }
        setReady(true);
      });
    return () => {
      cancelled = true;
    };
  }, [router]);

  if (!ready) return null;
  return <MagentoPackAdmin />;
}

export default function MagentoOperationsPage() {
  if (magentoVisualFixtureEnabled()) {
    return <MagentoPackAdmin fixture={MAGENTO_VISUAL_FIXTURE} />;
  }
  return (
    <AdminOnly>
      <MagentoOperationsGate />
    </AdminOnly>
  );
}
