"use client";

import { useEffect, useState } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import AppHeader from "@/components/AppHeader";
import { toast } from "sonner";
import Select from "@/components/Select";
import { Button } from "@/components/ui/Button";

const INPUT_CLS =
  "px-[13px] py-[11px] sm:px-3.5 sm:py-[13px] rounded-xl border-[1.5px] border-border bg-bg text-text text-base sm:text-[15px] font-body outline-none transition-all duration-200 focus:border-accent focus:shadow-[0_0_0_3px_rgba(107,92,231,0.08)]";
const LABEL_CLS = "text-[14px] font-semibold text-text";

// Quick-pick suggestions only — seats are free text (CV3 personas). A
// custom role flows through metrics, briefing tabs, and the persona
// prompt the same as these.
const SUGGESTED_SEATS = ["CEO", "CFO", "CRO", "COO", "CIO", "CPO"];
const MAX_SEAT_LENGTH = 40;
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
  const [customSeat, setCustomSeat] = useState("");
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

  const addCustomSeat = () => {
    const s = customSeat.trim().slice(0, MAX_SEAT_LENGTH);
    if (!s) return;
    setSeats((prev) =>
      prev.some((x) => x.toLowerCase() === s.toLowerCase()) ? prev : [...prev, s],
    );
    setCustomSeat("");
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
    <div className="root">
      <AppHeader />
      <div className="greet" style={{ marginTop: 8 }}>Let&apos;s set you up.</div>
      <div className="greet-sub">A couple of quick questions, then we&apos;ll do the rest.</div>

      <div style={{ display: "flex", flexDirection: "column", gap: 28, marginTop: 36 }}>
        <Field label="What's your company called?">
          <input
            className={INPUT_CLS}
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
            className={INPUT_CLS}
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

        <Field label="Which seats are active? Pick or add your own.">
          <div className="flex gap-[7px] flex-wrap">
            {[...SUGGESTED_SEATS, ...seats.filter((s) => !SUGGESTED_SEATS.includes(s))].map((s) => {
              const isOn = seats.includes(s);
              return (
                <button
                  key={s}
                  type="button"
                  onClick={() => toggleSeat(s)}
                  className={[
                    "px-4.5 py-2.5 rounded-full border-[1.5px] font-body text-[14.5px] font-medium cursor-pointer",
                    "transition-[color,background,border-color,transform,box-shadow] duration-200",
                    isOn
                      ? "bg-text border-text text-bg shadow-[0_2px_10px_rgba(20,18,12,0.18)]"
                      : "bg-white/60 border-border text-text2 hover:border-accent hover:text-accent hover:bg-accent-soft hover:-translate-y-px",
                  ].join(" ")}
                >
                  {s}
                </button>
              );
            })}
          </div>
          <div className="flex gap-[7px] mt-2">
            <input
              className={INPUT_CLS + " flex-1"}
              value={customSeat}
              maxLength={MAX_SEAT_LENGTH}
              placeholder="Add your own — e.g. Head of Ops, VP Sales"
              onChange={(e) => setCustomSeat(e.target.value)}
              onKeyDown={(e) => {
                if (e.key !== "Enter") return;
                e.preventDefault();
                addCustomSeat();
              }}
              aria-label="Add a custom seat"
            />
            <button
              type="button"
              onClick={addCustomSeat}
              disabled={!customSeat.trim()}
              className="px-4.5 rounded-xl border-[1.5px] border-border font-body text-[14.5px] font-medium cursor-pointer text-text2 hover:border-accent hover:text-accent disabled:opacity-40 disabled:cursor-default"
            >
              Add
            </button>
          </div>
        </Field>

        <Field label="Anything specific on your mind this quarter? (optional, one per line)">
          <textarea
            className={INPUT_CLS}
            value={prioritiesText}
            onChange={(e) => setPrioritiesText(e.target.value)}
            rows={3}
            placeholder={"Defend wholesale margins\nGrow DTC in Europe"}
            style={textareaExtras}
          />
        </Field>

        {error && <div style={{ color: "#c33" }}>{error}</div>}

        <Button
          variant="primary"
          disabled={!canSubmit}
          onClick={submit}
          className="self-start px-7 py-3.5 text-base"
        >
          {submitting ? "Setting up…" : "Build my briefing"}
        </Button>
      </div>
    </div>
  );
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label style={{ display: "flex", flexDirection: "column", gap: 8 }}>
      <span className={LABEL_CLS}>{label}</span>
      {children}
    </label>
  );
}

const textareaExtras: React.CSSProperties = {
  resize: "vertical",
  lineHeight: 1.55,
};
