"use client";

import { useState } from "react";
import { toast } from "sonner";
import AppHeader from "@/components/AppHeader";
import CreatorCredit from "@/components/CreatorCredit";
import PageHeading from "@/components/PageHeading";
import SectionNav from "@/components/SectionNav";
import { Button } from "@/components/ui/Button";
import { Checkbox } from "@/components/ui/Checkbox";
import { Field, Input } from "@/components/ui/Field";

type InstallPolicy = {
  allowUnverified: boolean;
  allowGitUrlInstalls: boolean;
  allowedMarketplaces: string[];
};

type InstallPolicyPayload = {
  policy: InstallPolicy;
  source: "org" | "default";
};

const OFFICIAL_MARKETPLACE_URL =
  "https://open-neko.github.io/plugins/marketplace.json";

export default function SecurityForm({ initial }: { initial: InstallPolicyPayload }) {
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
        <AppHeader back={{ href: "/admin/settings", label: "All settings" }}>
          <SectionNav current="admin" />
        </AppHeader>

        <PageHeading
          title="Security"
          description="Set the trust floor for plugin and skill installs. Every exception widens the agent’s install surface."
        />

        <section className="flex flex-col gap-6 mt-2">
          <Toggle
            label="Allow unverified installs"
            help="Lets operators run `openneko install <pkg> --unverified` (bypasses every marketplace). Use only for plugin authoring or emergency hotfixes — integrity comes from npm on trust."
            checked={policy.allowUnverified}
            onChange={(v) => toggle("allowUnverified", v)}
          />
  
          <Toggle
            label="Allow community-skill installs from git URLs"
            help="Lets operators run `openneko install <git-url>` to pull a skill directly from GitHub / GitLab / Codeberg. Skills are procedural knowledge the agent follows; any shell blocks run inside the agent's OpenShell sandbox."
            checked={policy.allowGitUrlInstalls}
            onChange={(v) => toggle("allowGitUrlInstalls", v)}
          />

          <Field
            label="Allowed marketplaces"
            htmlFor="marketplace-url"
            hint="Marketplaces this deployment trusts. The official OpenNeko marketplace is always trusted. Add community marketplaces by URL."
          >
            <ul className="flex flex-col gap-2 mt-1">
              {policy.allowedMarketplaces.map((url) => (
                <li
                  key={url}
                  className="flex items-center gap-2 px-3 py-2 rounded-lg border border-border bg-bg"
                >
                  <code className="flex-1 truncate text-ui-body-sm text-text2">{url}</code>
                  {url === OFFICIAL_MARKETPLACE_URL ? (
                    <span className="text-ui-caption text-text3">official</span>
                  ) : (
                    <Button
                      type="button"
                      variant="ghost"
                      size="sm"
                      onClick={() => removeMarketplace(url)}
                    >
                      Remove
                    </Button>
                  )}
                </li>
              ))}
            </ul>
            <div className="flex gap-2 mt-2">
              <Input
                id="marketplace-url"
                type="url"
                placeholder="https://example.com/marketplace.json"
                value={newMarketplace}
                onChange={(e) => setNewMarketplace(e.target.value)}
                className="flex-1"
                spellCheck={false}
              />
              <Button type="button" onClick={addMarketplace} variant="secondary">
                Add
              </Button>
            </div>
          </Field>
  
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
    <div className="grid gap-1.5">
      <Checkbox
        label={label}
        checked={checked}
        onChange={(e) => onChange(e.target.checked)}
      />
      <p className="pl-6 text-ui-caption leading-[var(--leading-compact)] text-text3">{help}</p>
    </div>
  );
}
