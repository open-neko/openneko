"use client";

import { Suspense, useEffect, useState } from "react";
import { useRouter } from "next/navigation";
import OnboardingWizard, { type WizardInitial } from "./OnboardingWizard";
import OnboardingUnavailable from "./OnboardingUnavailable";
import PersonaStep from "./PersonaStep";

type OnboardingLoadResult =
  | { kind: "settings" }
  | {
      kind: "persona";
      initialRoleTemplate: string;
      initialFocusAreas: string[];
    }
  | { kind: "workspace_pending" }
  | { kind: "wizard"; initial: WizardInitial; teamMode: boolean }
  | { kind: "error" };

export default function OnboardingPage() {
  const router = useRouter();
  const [result, setResult] = useState<OnboardingLoadResult | null>(null);

  useEffect(() => {
    let cancelled = false;
    void fetch("/api/v1/onboarding/wizard", { cache: "no-store" })
      .then(async (res) => {
        if (!res.ok) return { kind: "error" as const };
        return (await res.json()) as OnboardingLoadResult;
      })
      .catch(() => ({ kind: "error" as const }))
      .then((loaded) => {
        if (cancelled) return;
        if (loaded.kind === "settings") {
          router.replace("/admin/settings");
          return;
        }
        setResult(loaded);
      });
    return () => {
      cancelled = true;
    };
  }, [router]);

  if (!result || result.kind === "settings") {
    return null;
  }

  if (result.kind === "error") {
    return <OnboardingUnavailable />;
  }

  if (result.kind === "persona") {
    return (
      <Suspense fallback={null}>
        <PersonaStep
          initialRoleTemplate={result.initialRoleTemplate}
          initialFocusAreas={result.initialFocusAreas}
        />
      </Suspense>
    );
  }

  if (result.kind === "workspace_pending") {
    return <OnboardingUnavailable mode="workspace" />;
  }

  return (
    <Suspense fallback={null}>
      <OnboardingWizard initial={result.initial} teamMode={result.teamMode} />
    </Suspense>
  );
}
