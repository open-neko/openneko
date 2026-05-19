"use client";

import { Suspense, useEffect, useState } from "react";
import { useSearchParams } from "next/navigation";
import AppHeader from "@/components/AppHeader";
import CreatorCredit from "@/components/CreatorCredit";

interface ProviderInfo {
  pluginName: string;
  providerLabel: string;
}

function SignInBody() {
  const params = useSearchParams();
  const error = params.get("error");
  const returnTo = params.get("returnTo") ?? "/";
  const [provider, setProvider] = useState<ProviderInfo | null>(null);
  const [providerLoaded, setProviderLoaded] = useState(false);
  const [loginHint, setLoginHint] = useState("");

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const res = await fetch("/api/auth/status", { cache: "no-store" });
        const body = (await res.json()) as { provider: ProviderInfo | null };
        if (cancelled) return;
        setProvider(body.provider ?? null);
      } catch {
        if (cancelled) return;
        setProvider(null);
      } finally {
        if (!cancelled) setProviderLoaded(true);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, []);

  return (
    <div className="root">
      <AppHeader />

      <div className="max-w-[480px] mx-auto">
        <h1 className="greet">Sign in</h1>
        <p className="greet-sub">Access your OpenNeko deployment.</p>

        {error && (
          <div
            role="alert"
            className="mb-6 px-4 py-3.5 rounded-[14px] border border-danger-soft bg-danger-soft text-danger text-[13px] leading-[1.5]"
          >
            {error}
          </div>
        )}

        {!providerLoaded ? (
          <p className="text-text3 text-sm">Checking sign-in options…</p>
        ) : provider ? (
          <form
            action="/api/auth/begin"
            method="GET"
            className="settings-card"
          >
            <input type="hidden" name="returnTo" value={returnTo} />
            <label className="block">
              <span className="block font-display text-[12px] font-bold uppercase tracking-[0.12em] text-text3 mb-2">
                Email <span className="font-body normal-case tracking-normal text-text3 font-normal">(optional)</span>
              </span>
              <input
                type="email"
                name="loginHint"
                value={loginHint}
                onChange={(e) => setLoginHint(e.target.value)}
                placeholder="you@company.com"
                autoComplete="email"
                className="w-full px-3.5 py-3 rounded-[12px] border border-border bg-white text-text text-[14px] placeholder:text-text3 focus:outline-none focus:border-accent focus:ring-2 focus:ring-accent-soft transition-colors"
              />
              <span className="mt-2 block text-text3 text-[12px] leading-[1.5]">
                Speeds up routing to the right IdP behind {provider.providerLabel}.
              </span>
            </label>
            <button
              type="submit"
              className="mt-6 w-full px-4 py-3 rounded-[12px] bg-text text-bg font-display font-bold text-[15px] tracking-[-0.01em] hover:opacity-90 active:translate-y-px transition-all cursor-pointer"
            >
              Sign in with {provider.providerLabel}
            </button>
            <p className="mt-5 text-text3 text-[11px] tracking-[0.02em] text-center">
              Provided by <code className="font-mono text-text2">{provider.pluginName}</code>
            </p>
          </form>
        ) : (
          <div className="settings-card">
            <p className="font-display text-[20px] font-bold text-text mb-2 leading-tight">
              No SSO plugin is installed.
            </p>
            <p className="text-text2 text-[14px] leading-[1.6]">
              An operator with shell access can install one — for example{" "}
              <code className="font-mono text-text bg-neutral px-1.5 py-0.5 rounded-[6px] text-[13px]">
                openneko install @open-neko/plugin-scalekit
              </code>{" "}
              — to enable enterprise sign-in. Until then, OpenNeko runs in
              single-operator mode.
            </p>
          </div>
        )}
      </div>

      <CreatorCredit />
    </div>
  );
}

export default function SignInPage() {
  return (
    <Suspense fallback={null}>
      <SignInBody />
    </Suspense>
  );
}
