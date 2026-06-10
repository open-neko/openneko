"use client";

import { useState } from "react";
import { Clock } from "lucide-react";
import { formatHours, formatSavedShort, sinceLabel } from "@/lib/hours-saved";
import { Sparkline } from "@/components/Sparkline";

export type HoursSavedValue = {
  windowHours: number;
  windowMinutes: number;
  totalMinutes: number;
  windowTasks: number;
  dailyMinutes?: number[];
  sinceISO: string | null;
};

export type HoursSavedItem = {
  label: string;
  minutes: number;
  basis: string | null;
};

// The cumulative "hours saved" value-prop, with a methodology disclosure.
// Self-estimated numbers only earn trust when the reasoning is visible, so
// the "how?" panel spells out the method and lists recent items + their
// basis. See docs/HOURS_SAVED_PLAN.md.
export default function HoursSavedHero({
  value,
  items,
}: {
  value: HoursSavedValue;
  items: HoursSavedItem[];
}) {
  const [open, setOpen] = useState(false);
  const hero = formatHours(value.totalMinutes);
  const windowLabel = formatSavedShort(value.windowMinutes);
  const detailed = items.filter((i) => i.minutes > 0).slice(0, 6);
  const daily = value.dailyMinutes ?? [];
  const hasTrend = daily.some((d) => d > 0);

  return (
    <div className="mb-7" style={{ animation: "fadeUp 0.5s ease 0.12s both" }}>
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        aria-expanded={open}
        className="group inline-flex items-center gap-3.5 bg-accent-soft border border-accent/25 rounded-2xl pl-3 pr-4 py-2.5 cursor-pointer transition-[transform,box-shadow,border-color] duration-200 hover:-translate-y-px hover:shadow-hover hover:border-accent/45 text-left"
      >
        <span className="grid place-items-center w-9 h-9 rounded-xl bg-card shadow-soft text-accent flex-none">
          <Clock size={17} strokeWidth={2.25} />
        </span>
        <span className="font-display text-[26px] font-extrabold tracking-[-0.03em] text-accent tabular-nums leading-none">
          {hero.value}
          <span className="text-[13px] font-bold ml-1 opacity-70">{hero.unit}</span>
        </span>
        <span className="text-[12.5px] leading-[1.4] text-text2">
          saved {sinceLabel(value.sinceISO)}
          {windowLabel ? (
            <>
              <br />
              <b className="text-success-ink font-bold">+{windowLabel} in the last {value.windowHours}h</b>
              {value.windowTasks > 0 && (
                <> · {value.windowTasks} task{value.windowTasks === 1 ? "" : "s"} handled for you</>
              )}
            </>
          ) : null}
        </span>
        {hasTrend && (
          <span
            className="ml-1 flex-none self-center text-accent/70"
            title="Time saved per day, last 7 days"
          >
            <Sparkline values={daily} width={64} height={18} />
          </span>
        )}
        <span className="ml-1 font-mono text-[11px] font-semibold text-accent opacity-70 group-hover:opacity-100 whitespace-nowrap">
          how? {open ? "▾" : "→"}
        </span>
      </button>

      {open && (
        <div className="mt-2.5 max-w-[640px] bg-card border border-border rounded-2xl px-5 py-4 shadow-soft text-[13.5px] leading-[1.55] text-text2">
          <div className="font-display text-[11px] font-bold tracking-[0.13em] uppercase text-text3 mb-2.5">
            How we estimate hours saved
          </div>
          <p className="m-0 mb-2.5">
            Each time OpenNeko does something for you — sends a message, files a
            refund, pulls a report — it estimates how long that task would take a
            person to do by hand, and records a one-line reason. We estimate
            conservatively and cap every task, so the total is a{" "}
            <span className="text-text font-semibold">floor, not a flattering number</span>.
          </p>
          <p className="m-0">
            We only count work that actually happened: actions that fired and
            analyses we delivered — never anything still awaiting your approval.
          </p>

          {detailed.length > 0 && (
            <div className="mt-3.5 pt-3.5 border-t border-border">
              <div className="font-display text-[11px] font-bold tracking-[0.13em] uppercase text-text3 mb-2">
                Recent
              </div>
              <ul className="list-none m-0 p-0 grid gap-1.5">
                {detailed.map((item, i) => (
                  <li key={i} className="flex items-baseline gap-2.5 text-[12.5px]">
                    <span className="font-mono text-accent tabular-nums flex-none w-[58px]">
                      {formatSavedShort(item.minutes)}
                    </span>
                    <span className="text-text2 min-w-0">
                      <span className="text-text">{item.label}</span>
                      {item.basis && (
                        <span className="text-text3"> — {item.basis}</span>
                      )}
                    </span>
                  </li>
                ))}
              </ul>
            </div>
          )}
        </div>
      )}
    </div>
  );
}
