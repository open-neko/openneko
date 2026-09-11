"use client";

import { useState } from "react";
import { Clock } from "lucide-react";
import { formatHours, formatSavedShort, sinceLabel } from "@/lib/hours-saved";
import { Sparkline } from "@/components/Sparkline";
import { Button } from "@/components/ui/button";

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
//
// The row is a full-width instrument bar: it shares the briefing content
// column with the command strip above and the card grid below, so every
// stacked block lines up on one edge at every width.
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
      <Button
        variant="ghost"
        type="button"
        onClick={() => setOpen((o) => !o)}
        aria-expanded={open}
        className="group flex w-full flex-wrap items-center gap-x-4 gap-y-2 rounded-2xl border border-border bg-card px-5 py-4 text-left shadow-soft cursor-pointer transition-[transform,box-shadow,border-color] duration-200 hover:-translate-y-px hover:shadow-hover hover:border-accent/40"
      >
        <span className="grid place-items-center w-9 h-9 rounded-xl bg-accent-soft text-accent flex-none">
          <Clock size={17} strokeWidth={2.25} />
        </span>
        <span className="font-display text-[26px] font-extrabold tracking-[-0.03em] text-accent tabular-nums leading-none">
          {hero.value}
          <span className="text-ui-body-sm font-bold ml-1 opacity-70">
            {hero.unit}
          </span>
        </span>
        <span className="order-last basis-full min-w-0 whitespace-normal text-ui-body-sm leading-[1.4] text-text2 sm:order-none sm:basis-auto">
          saved {sinceLabel(value.sinceISO)}
          {windowLabel ? (
            <>
              {" · "}
              <b className="text-success-ink font-bold">
                +{windowLabel} in the last {value.windowHours}h
              </b>
              {value.windowTasks > 0 && (
                <>
                  {" · "}
                  {value.windowTasks} task{value.windowTasks === 1 ? "" : "s"}{" "}
                  handled for you
                </>
              )}
            </>
          ) : null}
        </span>
        <span
          aria-hidden="true"
          className="hidden flex-1 min-w-8 self-center h-px bg-border sm:block"
        />
        {hasTrend && (
          <span
            className="hidden flex-none self-center text-accent/70 sm:block"
            title="Time saved per day, last 7 days"
          >
            <Sparkline values={daily} width={64} height={18} />
          </span>
        )}
        <span className="ml-auto font-mono text-ui-label font-semibold text-accent opacity-70 group-hover:opacity-100 whitespace-nowrap sm:ml-0">
          how? {open ? "▾" : "→"}
        </span>
      </Button>

      {open && (
        <div className="mt-2.5 bg-card border border-border rounded-2xl px-5 py-4 shadow-soft text-ui-body leading-[1.55] text-text2">
          <div className="font-display text-ui-label font-bold tracking-[0.13em] uppercase text-text3 mb-2.5">
            How we estimate hours saved
          </div>
          <p className="m-0 mb-2.5">
            OpenNeko estimates how long each task would take a person by hand,
            then records a one-line reason. A task is any work it completes for
            you: a message sent, a refund filed, a report pulled. It counts
            conservatively and caps every task, so the total stays a{" "}
            <span className="text-text font-semibold">floor</span>.
          </p>
          <p className="m-0">
            We count only completed work: actions that fired and analyses we
            delivered. Work still awaiting your approval never counts.
          </p>

          {detailed.length > 0 && (
            <div className="mt-3.5 pt-3.5 border-t border-border">
              <div className="font-display text-ui-label font-bold tracking-[0.13em] uppercase text-text3 mb-2">
                Recent
              </div>
              <ul className="list-none m-0 p-0 grid gap-1.5">
                {detailed.map((item, i) => (
                  <li
                    key={i}
                    className="flex items-baseline gap-2.5 text-ui-body-sm"
                  >
                    <span className="font-mono text-accent tabular-nums flex-none w-[58px]">
                      {formatSavedShort(item.minutes)}
                    </span>
                    <span className="text-text2 min-w-0">
                      <span className="text-text">{item.label}</span>
                      {item.basis && (
                        <span className="text-text3"> · {item.basis}</span>
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
