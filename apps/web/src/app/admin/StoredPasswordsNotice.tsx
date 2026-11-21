"use client";

import Link from "next/link";
import { useEffect, useState } from "react";

type StoredSecret = { name: string; dbname?: string | null; login?: string | null };

export function savedBeforeUpgrade(secrets: StoredSecret[]): string[] {
  return secrets.filter((secret) => !secret.dbname || !secret.login).map((secret) => secret.name);
}

export function StoredPasswordsLine({ names }: { names: string[] }) {
  if (names.length === 0) return null;
  return (
    <p role="status" className="mt-6 mb-0 rounded-[8px] border border-border bg-bg px-3 py-2.5 text-ui-body-sm text-text2">
      {names.length === 1 ? "The stored source password " : "The stored source passwords "}
      {names.join(", ")} {names.length === 1 ? "was" : "were"} saved before this release with no database and login. OpenNeko does not send{" "}
      {names.length === 1 ? "it" : "them"} until an admin saves {names.length === 1 ? "it" : "each"} again at{" "}
      <Link href="/admin/settings/graphjin" className="text-accent underline">GraphJin config</Link>.
    </p>
  );
}

export function StoredPasswordsNotice() {
  const [names, setNames] = useState<string[]>([]);
  useEffect(() => {
    let cancelled = false;
    void fetch("/api/v1/settings/source-secrets", { cache: "no-store" })
      .then((r) => (r.ok ? (r.json() as Promise<{ secrets: StoredSecret[] }>) : { secrets: [] }))
      .then((result) => {
        if (!cancelled) setNames(savedBeforeUpgrade(result.secrets ?? []));
      })
      .catch(() => undefined);
    return () => {
      cancelled = true;
    };
  }, []);
  return <StoredPasswordsLine names={names} />;
}
