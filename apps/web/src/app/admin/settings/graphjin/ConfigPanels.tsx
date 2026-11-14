"use client";

import { useEffect, useState } from "react";
import { adminApi } from "@/components/admin/admin-api";
import { ConfigDescription, type ConfigDescriptionItem } from "./ConfigDescription";
import { ConfigFileView, type GraphjinConfigFileView } from "./ConfigFileView";

type ConfigView = { descriptions: ConfigDescriptionItem[]; file: GraphjinConfigFileView };
type Loaded = { ok: true; body: ConfigView } | { ok: false; error: string };

function Notice({ text, tone = "text-text2" }: { text: string; tone?: string }) {
  return (
    <section className="settings-card">
      <p className={`text-ui-body-sm ${tone}`}>{text}</p>
    </section>
  );
}

export default function ConfigPanels() {
  const [loaded, setLoaded] = useState<Loaded | null>(null);
  useEffect(() => {
    let cancelled = false;
    void adminApi<ConfigView>("/api/v1/admin/graphjin/config").then((result) => {
      if (!cancelled) setLoaded(result);
    });
    return () => {
      cancelled = true;
    };
  }, []);
  if (loaded === null) return <Notice text="Reading the GraphJin config…" />;
  if (!loaded.ok) {
    return <Notice tone="text-danger" text={`OpenNeko could not read the GraphJin config: ${loaded.error}`} />;
  }
  return (
    <>
      <ConfigDescription descriptions={loaded.body.descriptions} />
      <ConfigFileView view={loaded.body.file} />
    </>
  );
}
