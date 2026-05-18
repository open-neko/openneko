"use client";

import { Suspense, useEffect, useState } from "react";
import { useSearchParams } from "next/navigation";
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

  const signInHref = (() => {
    const url = new URL("/api/auth/begin", "http://placeholder");
    url.searchParams.set("returnTo", returnTo);
    if (loginHint.length > 0) {
      url.searchParams.set("loginHint", loginHint);
    }
    return `${url.pathname}${url.search}`;
  })();

  return (
    <div className="root">
      <main className="max-w-md mx-auto pt-24 pb-12 px-6">
        <h1 className="font-display text-3xl font-bold mb-2">Sign in</h1>
        <p className="text-text2 text-sm mb-8">
          Access your OpenNeko deployment.
        </p>

        {error && (
          <div
            role="alert"
            className="mb-6 px-4 py-3 rounded-[10px] border border-border bg-accent-soft text-accent text-sm"
          >
            {error}
          </div>
        )}

        {!providerLoaded ? (
          <div className="text-text3 text-sm">Checking sign-in options…</div>
        ) : provider ? (
          <form
            method="GET"
            action="/api/auth/begin"
            className="flex flex-col gap-3"
          >
            <input type="hidden" name="returnTo" value={returnTo} />
            <label className="flex flex-col gap-1.5 text-sm">
              <span className="text-text2">Email (optional)</span>
              <input
                type="email"
                name="loginHint"
                value={loginHint}
                onChange={(e) => setLoginHint(e.target.value)}
                placeholder="you@company.com"
                autoComplete="email"
                className="px-3 py-2.5 rounded-[10px] border border-border bg-white text-text text-sm focus:outline-none focus:border-accent"
              />
              <span className="text-text3 text-[12px]">
                Speeds up routing to the right IdP behind {provider.providerLabel}.
              </span>
            </label>
            <button
              type="submit"
              className="mt-2 px-4 py-2.5 rounded-[10px] bg-text text-bg font-medium text-sm hover:opacity-90 cursor-pointer"
            >
              Sign in with {provider.providerLabel}
            </button>
            <a
              href={signInHref}
              className="hidden"
              aria-hidden="true"
              tabIndex={-1}
            >
              {/* Mirror of the form's GET URL for keyboard/screen-reader users
                  who can't submit forms in this environment. */}
              Sign in with {provider.providerLabel}
            </a>
            <p className="text-text3 text-[12px] mt-2">
              Provided by the <code>{provider.pluginName}</code> plugin.
            </p>
          </form>
        ) : (
          <div className="rounded-[10px] border border-border p-4 text-sm text-text2 leading-[1.5]">
            <p className="mb-2">No SSO plugin is installed.</p>
            <p>
              An operator with shell access can install one — for example,{" "}
              <code>openneko install @open-neko/plugin-scalekit</code> — to
              enable enterprise sign-in. Until then, OpenNeko runs in
              single-operator mode.
            </p>
          </div>
        )}
      </main>
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
