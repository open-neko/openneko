"use client";

import { useEffect, useState } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import { toast } from "sonner";
import Select from "@/components/Select";

const ALL_SEATS = ["CEO", "CFO", "CRO", "COO", "CIO", "CPO"] as const;
const MONTHS = [
  "January", "February", "March", "April", "May", "June",
  "July", "August", "September", "October", "November", "December",
];
const MONTH_OPTIONS = MONTHS.map((label, i) => ({ value: String(i + 1), label }));

export type WizardInitial = {
  companyName: string;
  companyNote: string;
  fiscalYearStartMonth: number;
  activeSeats: string[];
  priorities: string[];
};

const EMPTY_INITIAL: WizardInitial = {
  companyName: "",
  companyNote: "",
  fiscalYearStartMonth: 1,
  activeSeats: [],
  priorities: [],
};

export default function OnboardingWizard({ initial = EMPTY_INITIAL }: { initial?: WizardInitial }) {
  const router = useRouter();
  const searchParams = useSearchParams();
  const [companyName, setCompanyName] = useState(initial.companyName);
  const [companyNote, setCompanyNote] = useState(initial.companyNote);
  const [fyMonth, setFyMonth] = useState(initial.fiscalYearStartMonth);
  const [seats, setSeats] = useState<string[]>(
    initial.activeSeats.length > 0 ? initial.activeSeats : ["CEO"],
  );
  const [prioritiesText, setPrioritiesText] = useState(initial.priorities.join("\n"));
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // If we landed here because the previous profile build failed, surface
  // the error as a toast. The URL only carries a flag (?failed=1); the
  // actual message comes from the server, so the toast text isn't
  // attacker-controllable. If the URL flag is forged but no real failure
  // exists, silently no-op.
  useEffect(() => {
    if (searchParams.get("failed") !== "1") return;
    let cancelled = false;
    (async () => {
      try {
        const res = await fetch("/api/onboarding/status");
        const status = await res.json();
        if (cancelled) return;
        if (status.state === "failed" && typeof status.message === "string") {
          toast.error("Setup failed", { description: status.message });
        }
      } catch {
        // network error — drop silently; user is already on the wizard
      } finally {
        // Strip the flag so a refresh doesn't re-fire the toast.
        if (!cancelled) router.replace("/onboarding");
      }
    })();
    return () => { cancelled = true; };
  }, [searchParams, router]);

  const toggleSeat = (s: string) => {
    setSeats((prev) =>
      prev.includes(s) ? prev.filter((x) => x !== s) : [...prev, s],
    );
  };

  const submit = async () => {
    setSubmitting(true);
    setError(null);
    try {
      const priorities = prioritiesText
        .split("\n")
        .map((s) => s.trim())
        .filter(Boolean);
      const res = await fetch("/api/onboarding/submit", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          companyName: companyName.trim(),
          companyNote,
          fiscalYearStartMonth: fyMonth,
          activeSeats: seats,
          priorities,
        }),
      });
      if (!res.ok) throw new Error(await res.text());
      router.push("/business-profile");
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
      setSubmitting(false);
    }
  };

  const canSubmit =
    companyName.trim().length > 0 &&
    companyNote.trim().length > 0 &&
    seats.length > 0 &&
    !submitting;

  return (
    <div className="root" style={{ paddingTop: 60 }}>
      <div className="brand">
        <img className="brand-icon" src="/cat.png" alt="" width={32} height={32} />
        <span className="brand-name">OpenNeko</span>
      </div>

      <div className="greet" style={{ marginTop: 32 }}>Let&apos;s set you up.</div>
      <div className="greet-sub">A couple of quick questions, then we&apos;ll do the rest.</div>

      <div style={{ display: "flex", flexDirection: "column", gap: 28, marginTop: 36 }}>
        <Field label="What's your company called?">
          <input
            className="settings-input"
            type="text"
            value={companyName}
            onChange={(e) => setCompanyName(e.target.value)}
            placeholder="AdventureWorks Cycles"
            autoComplete="organization"
            required
          />
        </Field>

        <Field label="In 2–3 sentences, what does your company do, who do you sell to, and what matters most this quarter?">
          <textarea
            className="settings-input"
            value={companyNote}
            onChange={(e) => setCompanyNote(e.target.value)}
            rows={4}
            placeholder="We make and sell bicycles to retailers across North America and Europe…"
            style={textareaExtras}
          />
        </Field>

        <Field label="When does your fiscal year start?">
          <Select
            value={String(fyMonth)}
            onChange={(v) => setFyMonth(Number(v))}
            options={MONTH_OPTIONS}
            ariaLabel="Fiscal year start month"
          />
        </Field>

        <Field label="Which CXO seats are active?">
          <div style={{ display: "flex", gap: 8, flexWrap: "wrap" }}>
            {ALL_SEATS.map((s) => (
              <button
                key={s}
                type="button"
                onClick={() => toggleSeat(s)}
                className={`pill${seats.includes(s) ? " on" : ""}`}
              >
                {s}
              </button>
            ))}
          </div>
        </Field>

        <Field label="Anything specific on your mind this quarter? (optional, one per line)">
          <textarea
            className="settings-input"
            value={prioritiesText}
            onChange={(e) => setPrioritiesText(e.target.value)}
            rows={3}
            placeholder={"Defend wholesale margins\nGrow DTC in Europe"}
            style={textareaExtras}
          />
        </Field>

        {error && <div style={{ color: "#c33" }}>{error}</div>}

        <button
          type="button"
          disabled={!canSubmit}
          onClick={submit}
          className="pill on"
          style={{ alignSelf: "flex-start", padding: "14px 28px", fontSize: 16, opacity: canSubmit ? 1 : 0.5 }}
        >
          {submitting ? "Setting up…" : "Build my briefing"}
        </button>
      </div>
    </div>
  );
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label style={{ display: "flex", flexDirection: "column", gap: 8 }}>
      <span className="settings-label">{label}</span>
      {children}
    </label>
  );
}

const textareaExtras: React.CSSProperties = {
  resize: "vertical",
  lineHeight: 1.55,
};
