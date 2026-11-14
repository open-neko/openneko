"use client";

import { useEffect, useState, type ReactNode } from "react";
import { AdminError } from "@/components/admin/AdminError";
import { adminApi } from "@/components/admin/admin-api";
import { SkeletonList } from "@/components/ui/skeleton";
import AdminOnly from "./AdminOnly";
import { AdminShell } from "./AdminShell";

type Loaded = { ok: true; bodies: unknown[] } | { ok: false; error: string };

function Loader({ title, paths, children }: AdminLoadProps) {
  const [loaded, setLoaded] = useState<Loaded | null>(null);
  const key = paths.join("\n");
  useEffect(() => {
    let cancelled = false;
    void Promise.all(key.split("\n").map((path) => adminApi(path))).then((results) => {
      if (cancelled) return;
      const failed = results.find((result) => !result.ok);
      setLoaded(
        failed && !failed.ok
          ? { ok: false, error: failed.error }
          : { ok: true, bodies: results.map((result) => (result.ok ? result.body : null)) },
      );
    });
    return () => {
      cancelled = true;
    };
  }, [key]);
  if (loaded === null) {
    return (
      <div className="root">
        <SkeletonList />
      </div>
    );
  }
  if (!loaded.ok) {
    return (
      <AdminShell title={title} back={{ href: "/admin/settings", label: "Settings" }}>
        <AdminError message={loaded.error} />
      </AdminShell>
    );
  }
  return <>{children(loaded.bodies)}</>;
}

type AdminLoadProps = {
  title: string;
  paths: string[];
  children: (bodies: unknown[]) => ReactNode;
};

export default function AdminLoad(props: AdminLoadProps) {
  return (
    <AdminOnly>
      <Loader {...props} />
    </AdminOnly>
  );
}
