"use client";

import { useState } from "react";
import EntryShell from "@/components/EntryShell";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";

type Outcome = { approved: boolean; name: string } | null;

export function DeviceApproval({
  initialCode,
  deviceName,
  codeNotFound,
}: {
  initialCode: string;
  deviceName: string | null;
  codeNotFound: boolean;
}) {
  const [code, setCode] = useState(initialCode);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(
    codeNotFound ? "This code is not valid or has expired. Start again on the device." : null,
  );
  const [outcome, setOutcome] = useState<Outcome>(null);

  async function decide(approve: boolean) {
    setBusy(true);
    setError(null);
    try {
      const res = await fetch("/api/v1/auth/device/approve", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ userCode: code, approve }),
      });
      if (res.status === 401) {
        const trimmed = code.trim();
        const returnTo = trimmed ? `/device?code=${encodeURIComponent(trimmed)}` : "/device";
        window.location.assign(`/signin?returnTo=${encodeURIComponent(returnTo)}`);
        return;
      }
      const body = (await res.json().catch(() => ({}))) as { error?: string; name?: string };
      if (!res.ok) setError(body.error ?? `The request failed (${res.status}).`);
      else setOutcome({ approved: approve, name: body.name ?? "the device" });
    } catch {
      setError("OpenNeko did not answer. Try again.");
    } finally {
      setBusy(false);
    }
  }

  return (
    <EntryShell
      title="Sign in on a device."
      description="Approve the code that the OpenNeko app shows. The device then stays signed in until you sign out on it or an administrator revokes it."
    >
      <div className="entry-section-head">
        <p className="entry-section-kicker">Device sign-in</p>
        <h2>{deviceName ? `Approve ${deviceName}` : "Enter the code"}</h2>
        <p>Check that this code matches the code on your device.</p>
      </div>

      {error ? (
        <div className="entry-error" role="alert">
          {error}
        </div>
      ) : null}

      {outcome ? (
        <div className="entry-system-state" role="status">
          {outcome.approved
            ? `${outcome.name} is signed in. You can close this page.`
            : `You denied ${outcome.name}. It is not signed in.`}
        </div>
      ) : (
        <form
          className="entry-fields"
          onSubmit={(event) => {
            event.preventDefault();
            void decide(true);
          }}
        >
          <label className="entry-field">
            <span>Code</span>
            <Input
              name="code"
              value={code}
              onChange={(event) => setCode(event.target.value.toUpperCase())}
              placeholder="BCDF-GHJK"
              autoComplete="one-time-code"
              autoCapitalize="characters"
              spellCheck={false}
              required
              className="entry-control"
            />
          </label>
          <Button type="submit" variant="primary" size="lg" className="w-full" disabled={busy || !code.trim()}>
            {busy ? "Working…" : "Approve this device"}
          </Button>
          <Button type="button" variant="secondary" size="lg" className="w-full" disabled={busy || !code.trim()} onClick={() => void decide(false)}>
            Deny
          </Button>
        </form>
      )}
    </EntryShell>
  );
}
