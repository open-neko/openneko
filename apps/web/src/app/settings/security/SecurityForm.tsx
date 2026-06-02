"use client";

import { useState } from "react";
import { useRouter } from "next/navigation";
import { toast } from "sonner";
import AppHeader from "@/components/AppHeader";
import CreatorCredit from "@/components/CreatorCredit";
import SectionNav from "@/components/SectionNav";
import { Button } from "@/components/ui/Button";

type InstallPolicy = {
  allowUnverified: boolean;
  allowGitUrlInstalls: boolean;
  allowedMarketplaces: string[];
  allowSandboxedSkillEscape: boolean;
};

type InstallPolicyPayload = {
  policy: InstallPolicy;
  source: "org" | "default";
};

const OFFICIAL_MARKETPLACE_URL =
  "https://open-neko.github.io/plugins/marketplace.json";

const FIELD_CLS = "flex flex-col gap-2";
const LABEL_CLS = "text-[14px] font-semibold text-text";
const HELP_CLS = "text-[13px] text-text3 leading-[1.45]";
const INPUT_CLS =
  "px-[13px] py-[11px] sm:px-3.5 sm:py-[13px] rounded-xl border-[1.5px] border-border bg-bg text-text text-base sm:text-[15px] font-body outline-none transition-all duration-200 focus:border-accent focus:shadow-[0_0_0_3px_rgba(107,92,231,0.08)]";

export default function SecurityForm({ initial }: { initial: InstallPolicyPayload }) {
  const router = useRouter();
  const [policy, setPolicy] = useState<InstallPolicy>(initial.policy);
  const [newMarketplace, setNewMarketplace] = useState("");
  const [saving, setSaving] = useState(false);

  function toggle<K extends keyof InstallPolicy>(key: K, value: InstallPolicy[K]) {
    setPolicy((p) => ({ ...p, [key]: value }));
  }

  function removeMarketplace(url: string) {
    if (url === OFFICIAL_MARKETPLACE_URL) {
      toast.error("The official marketplace can't be removed.");
      return;
    }
    setPolicy((p) => ({
      ...p,
      allowedMarketplaces: p.allowedMarketplaces.filter((m) => m !== url),
    }));
  }

  function addMarketplace() {
    const trimmed = newMarketplace.trim();
    if (!trimmed) return;
    try {
      const u = new URL(trimmed);
      if (u.protocol !== "https:") {
        toast.error("Marketplace URLs must use https://");
        return;
      }
    } catch {
      toast.error("Not a valid URL.");
      return;
    }
    if (policy.allowedMarketplaces.includes(trimmed)) {
      toast.error("Already in the list.");
      return;
    }
    setPolicy((p) => ({
      ...p,
      allowedMarketplaces: [...p.allowedMarketplaces, trimmed],
    }));
    setNewMarketplace("");
  }

  async function save() {
    setSaving(true);
    try {
      const res = await fetch("/api/settings/install-policy", {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(policy),
      });
      if (!res.ok) {
        const body = (await res.json().catch(() => null)) as { error?: string } | null;
        throw new Error(body?.error ?? `HTTP ${res.status}`);
      }
      toast.success("Install policy saved.");
    } catch (e) {
      toast.error(e instanceof Error ? e.message : String(e));
    } finally {
      setSaving(false);
    }
  }

  return (
    <>
      <div
        className="root"
        style={{ "--page-width": "min(1000px, 100%)" } as React.CSSProperties}
      >
        <AppHeader>
          <SectionNav current="settings" />
        </AppHeader>

        <div className="mt-1 mb-3.5 font-mono text-[12.5px] text-text3 flex items-center gap-2">
          <button
            type="button"
            className="bg-transparent border-0 text-text3 cursor-pointer font-[inherit] p-0 hover:text-accent"
            onClick={() => router.push("/settings")}
          >
            ← Settings
          </button>
          <span className="opacity-50">/</span>
          <span>Security</span>
        </div>

        <div className="flex items-baseline justify-between gap-4 mb-3">
          <h1 className="font-display text-3xl font-extrabold tracking-[-0.02em] text-text">
            Security
          </h1>
        </div>

        <p className="text-sm leading-normal text-text2 mb-6 max-w-[640px]">
          Trust floor for plugin and skill installs. Defaults are
          secure-by-default — flip a switch on to widen the install surface, off
          to narrow it back.
        </p>

        <section className="flex flex-col gap-6 mt-2">
          <Toggle
            label="Allow unverified installs"
            help="Lets operators run `openneko install <pkg> --unverified` (bypasses every marketplace). Use only for plugin authoring or emergency hotfixes — integrity comes from npm on trust."
            checked={policy.allowUnverified}
            onChange={(v) => toggle("allowUnverified", v)}
          />
  
          <Toggle
            label="Allow community-skill installs from git URLs"
            help="Lets operators run `openneko install <git-url>` to pull a skill directly from GitHub / GitLab / Codeberg. Skills run in-process with the worker by default — combine with the sandbox-escape switch below for untrusted sources."
            checked={policy.allowGitUrlInstalls}
            onChange={(v) => toggle("allowGitUrlInstalls", v)}
          />
  
          <Toggle
            label="Sandbox shell blocks of untrusted skills"
            help="When installing a skill from a non-trusted source, run any shell blocks in its body inside a one-shot microVM. Slower but contained. Recommended when allowing git-URL installs."
            checked={policy.allowSandboxedSkillEscape}
            onChange={(v) => toggle("allowSandboxedSkillEscape", v)}
          />
  
          <div className={FIELD_CLS}>
            <label className={LABEL_CLS}>Allowed marketplaces</label>
            <p className={HELP_CLS}>
              Marketplaces this deployment trusts. The official OpenNeko
              marketplace is always trusted. Add community marketplaces by
              URL.
            </p>
            <ul className="flex flex-col gap-2 mt-1">
              {policy.allowedMarketplaces.map((url) => (
                <li
                  key={url}
                  className="flex items-center gap-2 px-3 py-2 rounded-lg border border-border bg-bg"
                >
                  <code className="flex-1 truncate text-[13px] text-text2">{url}</code>
                  {url === OFFICIAL_MARKETPLACE_URL ? (
                    <span className="text-[12px] text-text3">official</span>
                  ) : (
                    <button
                      type="button"
                      className="text-[12px] text-text2 underline"
                      onClick={() => removeMarketplace(url)}
                    >
                      remove
                    </button>
                  )}
                </li>
              ))}
            </ul>
            <div className="flex gap-2 mt-2">
              <input
                type="url"
                placeholder="https://example.com/marketplace.json"
                value={newMarketplace}
                onChange={(e) => setNewMarketplace(e.target.value)}
                className={`${INPUT_CLS} flex-1`}
              />
              <Button type="button" onClick={addMarketplace} variant="secondary">
                Add
              </Button>
            </div>
          </div>
  
          <div className="flex justify-end mt-2">
            <Button type="button" onClick={save} disabled={saving}>
              {saving ? "Saving…" : "Save"}
            </Button>
          </div>
        </section>
      </div>

      <CreatorCredit />
    </>
  );
}

function Toggle({
  label,
  help,
  checked,
  onChange,
}: {
  label: string;
  help: string;
  checked: boolean;
  onChange: (v: boolean) => void;
}) {
  return (
    <div className={FIELD_CLS}>
      <label className="flex items-start gap-3 cursor-pointer">
        <input
          type="checkbox"
          checked={checked}
          onChange={(e) => onChange(e.target.checked)}
          className="mt-1 w-4 h-4 cursor-pointer"
        />
        <div className="flex flex-col gap-1">
          <span className={LABEL_CLS}>{label}</span>
          <span className={HELP_CLS}>{help}</span>
        </div>
      </label>
    </div>
  );
}
