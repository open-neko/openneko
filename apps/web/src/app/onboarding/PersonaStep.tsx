"use client";

import { useState } from "react";
import { useRouter } from "next/navigation";

const INPUT_CLS =
  "px-[13px] py-[11px] sm:px-3.5 sm:py-[13px] rounded-xl border-[1.5px] border-border bg-bg text-text text-base sm:text-[15px] font-body outline-none transition-all duration-200 focus:border-accent focus:shadow-[0_0_0_3px_rgba(107,92,231,0.08)] w-full";

/**
 * Per-user onboarding (CV3): members joining an already-onboarded org
 * describe their role in their own words. Writes their operator_profile
 * row; the agent's <operator-profile> block picks it up on their next run.
 */
export default function PersonaStep({ initialRoleTemplate }: { initialRoleTemplate: string }) {
  const router = useRouter();
  const [roleTemplate, setRoleTemplate] = useState(initialRoleTemplate);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const submit = async () => {
    setSubmitting(true);
    setError(null);
    try {
      const res = await fetch("/api/settings/persona", {
        method: "PUT",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ roleTemplate }),
      });
      if (!res.ok) throw new Error(`save failed (${res.status})`);
      router.push("/");
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
      setSubmitting(false);
    }
  };

  return (
    <div className="max-w-[560px] mx-auto px-5 py-12">
      <h1 className="font-display text-[26px] font-extrabold tracking-[-0.02em] text-text">
        What do you do here?
      </h1>
      <p className="text-[14.5px] text-text2 mt-2 leading-[1.55]">
        Your workspace is already set up. Describe your role in your own words —
        briefings, answers, and priorities get tailored to it. You can change
        this anytime in Settings.
      </p>
      <textarea
        className={INPUT_CLS + " mt-5"}
        rows={4}
        value={roleTemplate}
        onChange={(e) => setRoleTemplate(e.target.value)}
        placeholder={
          "e.g. I run EU wholesale operations — I care about stock-outs, reorder lead times, and margin on the top 50 SKUs."
        }
        aria-label="Describe your role"
      />
      {error && <div className="text-[13px] text-red-600 mt-2">{error}</div>}
      <div className="flex gap-3 mt-5">
        <button
          type="button"
          onClick={submit}
          disabled={submitting || !roleTemplate.trim()}
          className="px-5 py-2.5 rounded-full bg-text text-bg font-body text-[14.5px] font-semibold cursor-pointer disabled:opacity-40 disabled:cursor-default"
        >
          {submitting ? "Saving…" : "Save and continue"}
        </button>
        <button
          type="button"
          onClick={() => router.push("/")}
          className="px-5 py-2.5 rounded-full border-[1.5px] border-border text-text2 font-body text-[14.5px] font-medium cursor-pointer hover:border-accent hover:text-accent"
        >
          Skip for now
        </button>
      </div>
    </div>
  );
}
