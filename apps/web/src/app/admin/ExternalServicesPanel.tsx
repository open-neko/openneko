"use client";

import { useCallback, useEffect, useState } from "react";
import { Button } from "@/components/ui/button";
import { StatCard, StatGrid } from "@/components/ui/stat-card";

type ExternalServiceStatus = {
  id: string;
  label: string;
  state: "off" | "ok" | "failing";
  where: string | null;
  detail: string;
  checkedAt: string;
};

const STATE_WORDS = { ok: "On", off: "Off", failing: "Failing" } as const;

export function ExternalServicesPanel() {
  const [services, setServices] = useState<ExternalServiceStatus[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [checking, setChecking] = useState(true);

  const show = useCallback((result: ExternalServiceStatus[] | string) => {
    if (typeof result === "string") setError(result);
    else setServices(result);
    setChecking(false);
  }, []);

  useEffect(() => {
    void fetchServices(false).then(show);
  }, [show]);

  const checkAgain = () => {
    setChecking(true);
    setError(null);
    void fetchServices(true).then(show);
  };

  return (
    <section className="mt-7" aria-busy={checking}>
      <div className="mb-4 flex flex-wrap items-center justify-between gap-3">
        <h2 className="font-display text-ui-caption font-semibold text-text3">External services</h2>
        <Button size="sm" onClick={checkAgain} disabled={checking}>
          {checking ? "Checking..." : "Check again"}
        </Button>
      </div>
      {error ? <p className="mb-3 text-ui-caption text-danger">The check failed: {error}</p> : null}
      <StatGrid label="External services">
        {services
          ? services.map((service) => (
              <StatCard
                key={service.id}
                label={service.label}
                value={STATE_WORDS[service.state]}
                tone={service.state === "ok" ? "ok" : service.state === "failing" ? "warn" : "neutral"}
                detail={
                  <span className="block break-words">
                    {service.where ? <span className="block font-semibold">{service.where}</span> : null}
                    {service.detail}
                  </span>
                }
              />
            ))
          : checking
            ? ["Inference", "Embedding", "Sandbox", "File store"].map((label) => (
                <StatCard key={label} label={label} value="..." detail="Checking" />
              ))
            : null}
      </StatGrid>
    </section>
  );
}

async function fetchServices(refresh: boolean): Promise<ExternalServiceStatus[] | string> {
  try {
    const res = await fetch(refresh ? "/api/v1/admin/services?refresh=1" : "/api/v1/admin/services", { cache: "no-store" });
    const body = (await res.json()) as { services?: ExternalServiceStatus[]; error?: string };
    if (!res.ok || !body.services) return body.error || `HTTP ${res.status}`;
    return body.services;
  } catch (err) {
    return err instanceof Error ? err.message : String(err);
  }
}
