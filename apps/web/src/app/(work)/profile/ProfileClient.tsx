"use client";

import { useEffect, useState } from "react";
import { Button } from "@/components/ui/button";
import { Field, Input } from "@/components/ui/field";
import { Textarea } from "@/components/ui/textarea";
import {
  deletePasskey,
  listPasskeys,
  type PasskeySummary,
  registerPasskey,
} from "@/lib/passkey";
import {
  getLocalPushEndpoint,
  listPushSubscriptions,
  type PushSubscriptionSummary,
  subscribeDeviceToWebPush,
  unsubscribeDeviceFromWebPush,
} from "@/lib/web-push";

type Profile = {
  displayName: string | null;
  roleTemplate: string;
  focusAreas: string[];
};

/**
 * A person's own account: the persona their agent runs read, passkeys for
 * approving actions, Web Push notifications for this device, plus sign out.
 * Members edit only their own row; the persona, passkey and push APIs enforce
 * that.
 */
export function ProfileClient({ email, signInEnabled }: { email: string; signInEnabled: boolean }) {
  const [displayName, setDisplayName] = useState("");
  const [roleTemplate, setRoleTemplate] = useState("");
  const [focusAreas, setFocusAreas] = useState("");
  const [state, setState] = useState<"loading" | "ready" | "saving">("loading");
  const [error, setError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);
  const [passkeys, setPasskeys] = useState<PasskeySummary[]>([]);
  const [passkeyName, setPasskeyName] = useState("");
  const [passkeyBusy, setPasskeyBusy] = useState(false);
  const [passkeyError, setPasskeyError] = useState<string | null>(null);
  const [pushSubscriptions, setPushSubscriptions] = useState<PushSubscriptionSummary[]>([]);
  const [localPushEndpoint, setLocalPushEndpoint] = useState<string | null>(null);
  const [pushBusy, setPushBusy] = useState(false);
  const [pushError, setPushError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    void (async () => {
      try {
        const res = await fetch("/api/v1/settings/persona", { cache: "no-store" });
        if (!res.ok) throw new Error(`could not load your profile (${res.status})`);
        const body = (await res.json()) as { profile: Profile | null };
        if (cancelled) return;
        // A person who has never saved a persona has no row yet.
        setDisplayName(body.profile?.displayName ?? "");
        setRoleTemplate(body.profile?.roleTemplate ?? "");
        setFocusAreas((body.profile?.focusAreas ?? []).join("\n"));
      } catch (e) {
        if (!cancelled) setError(e instanceof Error ? e.message : String(e));
      } finally {
        if (!cancelled) setState("ready");
      }
      try {
        const items = await listPasskeys();
        if (!cancelled) setPasskeys(items);
      } catch {
        // Keep the profile usable even if passkeys fail to load.
      }
      try {
        const [subs, endpoint] = await Promise.all([
          listPushSubscriptions(),
          getLocalPushEndpoint(),
        ]);
        if (!cancelled) {
          setPushSubscriptions(subs);
          setLocalPushEndpoint(endpoint);
        }
      } catch {
        // Keep the profile usable even if push subscriptions fail to load.
      }
    })();
    return () => {
      cancelled = true;
    };
  }, []);

  async function addPasskey() {
    setPasskeyBusy(true);
    setPasskeyError(null);
    try {
      const created = await registerPasskey(passkeyName.trim() || "This device");
      setPasskeys((current) => [created, ...current.filter((p) => p.id !== created.id)]);
      setPasskeyName("");
    } catch (e) {
      setPasskeyError(e instanceof Error ? e.message : String(e));
    } finally {
      setPasskeyBusy(false);
    }
  }

  async function removePasskey(id: string) {
    setPasskeyBusy(true);
    setPasskeyError(null);
    try {
      await deletePasskey(id, passkeys.length === 1);
      setPasskeys((current) => current.filter((p) => p.id !== id));
    } catch (e) {
      setPasskeyError(e instanceof Error ? e.message : String(e));
    } finally {
      setPasskeyBusy(false);
    }
  }

  const currentDevicePush = localPushEndpoint
    ? (pushSubscriptions.find((sub) => sub.endpoint === localPushEndpoint) ?? null)
    : null;
  const otherDevicePushes = pushSubscriptions.filter(
    (sub) => sub.id !== currentDevicePush?.id,
  );

  async function turnOnPush() {
    setPushBusy(true);
    setPushError(null);
    try {
      const created = await subscribeDeviceToWebPush();
      setLocalPushEndpoint(created.endpoint);
      setPushSubscriptions((current) => [
        created,
        ...current.filter((item) => item.id !== created.id && item.endpoint !== created.endpoint),
      ]);
    } catch (e) {
      setPushError(e instanceof Error ? e.message : String(e));
    } finally {
      setPushBusy(false);
    }
  }

  async function turnOffPush(sub: PushSubscriptionSummary) {
    setPushBusy(true);
    setPushError(null);
    try {
      await unsubscribeDeviceFromWebPush(sub.id, sub.endpoint);
      setPushSubscriptions((current) => current.filter((item) => item.id !== sub.id));
      if (localPushEndpoint === sub.endpoint) {
        setLocalPushEndpoint(null);
      }
    } catch (e) {
      setPushError(e instanceof Error ? e.message : String(e));
    } finally {
      setPushBusy(false);
    }
  }

  async function save() {
    setState("saving");
    setError(null);
    setSaved(false);
    try {
      const res = await fetch("/api/v1/settings/persona", {
        method: "PUT",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({
          displayName: displayName.trim() || null,
          roleTemplate: roleTemplate.trim(),
          focusAreas: focusAreas.split("\n").map((area) => area.trim()).filter(Boolean),
        }),
      });
      if (!res.ok) {
        const body = (await res.json().catch(() => null)) as { error?: string } | null;
        throw new Error(body?.error ?? `save failed (${res.status})`);
      }
      setSaved(true);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setState("ready");
    }
  }

  async function signOut() {
    try {
      await fetch("/api/v1/auth/logout", { method: "POST" });
    } finally {
      window.location.href = "/signin";
    }
  }

  return (
    <section className="settings-card max-w-(--form-max)">
      <div className="settings-card-head">
        <div>
          <h2 className="settings-card-title">Your profile</h2>
          <p className="settings-card-copy">
            Your agent runs read this. It belongs to your account, so it changes nobody else&apos;s workspace.
          </p>
        </div>
        {email ? (
          <div className="settings-source">
            <strong className="is-ok">{email}</strong>
          </div>
        ) : null}
      </div>

      {error ? (
        <div className="mb-3 rounded-control bg-danger-soft px-3 py-2 text-sm text-danger" role="alert">
          {error}
        </div>
      ) : null}

      <div className="flex flex-col gap-4">
        <Field label="Display name" htmlFor="profile-display-name">
          <Input
            id="profile-display-name"
            value={displayName}
            maxLength={120}
            disabled={state === "loading"}
            onChange={(event) => setDisplayName(event.target.value)}
            placeholder="How your name appears in runs"
          />
        </Field>
        <Field label="Your role" htmlFor="profile-role">
          <Input
            id="profile-role"
            value={roleTemplate}
            maxLength={120}
            disabled={state === "loading"}
            onChange={(event) => setRoleTemplate(event.target.value)}
            placeholder="Head of EU wholesale operations"
          />
        </Field>
        <Field label="What needs your attention?" htmlFor="profile-focus" hint="One priority per line.">
          <Textarea
            id="profile-focus"
            rows={5}
            value={focusAreas}
            disabled={state === "loading"}
            onChange={(event) => setFocusAreas(event.target.value)}
            placeholder={"Stock-outs on top SKUs\nReorder lead times\nWholesale margin"}
          />
        </Field>
        <div className="flex items-center justify-end gap-3">
          {saved ? <span className="text-sm text-text2">Saved.</span> : null}
          <Button variant="primary" disabled={state !== "ready"} onClick={() => void save()}>
            {state === "saving" ? "Saving…" : "Save profile"}
          </Button>
        </div>
      </div>

      <div className="mt-6 border-t border-border pt-5">
        <h3 className="text-ui-body font-semibold text-text">Passkeys</h3>
        <p className="mt-1 text-ui-body-sm text-text2">
          Register a passkey on your phone or computer to approve actions with a biometric check.
        </p>
        <p className="mt-1 text-ui-body-sm text-text2" data-testid="passkey-approval-rule">
          After you add a passkey, each approval in the browser asks for it.
        </p>

        {passkeyError ? (
          <div className="mt-3 rounded-control bg-danger-soft px-3 py-2 text-sm text-danger" role="alert">
            {passkeyError}
          </div>
        ) : null}

        <div className="mt-3 flex flex-wrap items-end gap-2.5">
          <div className="min-w-0 flex-1">
            <Field label="Passkey name" htmlFor="profile-passkey-name">
              <Input
                id="profile-passkey-name"
                value={passkeyName}
                maxLength={80}
                disabled={passkeyBusy}
                onChange={(event) => setPasskeyName(event.target.value)}
                placeholder="This phone"
              />
            </Field>
          </div>
          <Button
            variant="secondary"
            disabled={passkeyBusy}
            onClick={() => void addPasskey()}
          >
            {passkeyBusy ? "Working…" : "Add passkey"}
          </Button>
        </div>

        {passkeys.length > 0 ? (
          <ul className="mt-3 grid list-none gap-2 p-0 m-0">
            {passkeys.map((pk) => (
              <li
                key={pk.id}
                className="flex flex-wrap items-center justify-between gap-2 rounded-control border border-border bg-bg px-3 py-2 text-ui-body-sm"
              >
                <div className="min-w-0">
                  <div className="font-semibold text-text">{pk.name}</div>
                  <div className="text-ui-caption text-text3">
                    {pk.lastUsedAt ? "Used for approval" : "Registered"}
                  </div>
                </div>
                <Button
                  size="sm"
                  variant="ghost"
                  disabled={passkeyBusy}
                  onClick={() => void removePasskey(pk.id)}
                >
                  Remove
                </Button>
              </li>
            ))}
          </ul>
        ) : null}
      </div>

      <div className="mt-6 border-t border-border pt-5">
        <h3 className="text-ui-body font-semibold text-text">Push notifications</h3>
        <p className="mt-1 text-ui-body-sm text-text2">
          Receive a short notice in this browser when an approval waits, a run finishes, or a workflow needs attention.
        </p>

        {pushError ? (
          <div className="mt-3 rounded-control bg-danger-soft px-3 py-2 text-sm text-danger" role="alert">
            {pushError}
          </div>
        ) : null}

        <div className="mt-3 flex flex-wrap items-center justify-between gap-2.5">
          <span className="text-ui-body-sm text-text2" data-testid="push-status">
            {currentDevicePush
              ? "Notifications are on in this browser."
              : "Notifications are off in this browser."}
          </span>
          {currentDevicePush ? (
            <Button
              variant="secondary"
              disabled={pushBusy}
              onClick={() => void turnOffPush(currentDevicePush)}
            >
              {pushBusy ? "Working…" : "Turn off notifications"}
            </Button>
          ) : (
            <Button
              variant="secondary"
              disabled={pushBusy}
              onClick={() => void turnOnPush()}
            >
              {pushBusy ? "Working…" : "Turn on notifications"}
            </Button>
          )}
        </div>

        {otherDevicePushes.length > 0 ? (
          <ul className="mt-3 grid list-none gap-2 p-0 m-0">
            {otherDevicePushes.map((sub, idx) => (
              <li
                key={sub.id}
                className="flex flex-wrap items-center justify-between gap-2 rounded-control border border-border bg-bg px-3 py-2 text-ui-body-sm"
              >
                <div className="min-w-0">
                  <div className="font-semibold text-text">
                    Subscribed device {idx + 1}
                  </div>
                </div>
                <Button
                  size="sm"
                  variant="ghost"
                  disabled={pushBusy}
                  onClick={() => void turnOffPush(sub)}
                >
                  Turn off
                </Button>
              </li>
            ))}
          </ul>
        ) : null}
      </div>

      {signInEnabled ? (
        <div className="mt-6 border-t border-border pt-4">
          <Button variant="danger" onClick={() => void signOut()}>
            Sign out
          </Button>
        </div>
      ) : null}
    </section>
  );
}
