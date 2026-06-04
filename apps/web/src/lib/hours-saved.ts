// Formatting for the agent-estimated "hours saved" metric. Numbers are
// approximate by nature (see docs/HOURS_SAVED_PLAN.md) — always rendered
// with a "~" so the estimate framing is unmistakable.

/** Compact per-item label, e.g. "~8 min", "~1.5h". */
export function formatSavedShort(minutes: number): string {
  if (!Number.isFinite(minutes) || minutes <= 0) return "";
  if (minutes < 60) return `~${Math.round(minutes)} min`;
  const hours = minutes / 60;
  const rounded = hours >= 10 ? Math.round(hours) : Math.round(hours * 10) / 10;
  return `~${rounded}h`;
}

/** Big hero figure split into value + unit, e.g. { value: "142", unit: "hrs" }. */
export function formatHours(minutes: number): { value: string; unit: string } {
  const hours = Math.max(0, minutes) / 60;
  if (hours < 1) {
    return { value: String(Math.round(minutes)), unit: minutes === 1 ? "min" : "min" };
  }
  const rounded = hours >= 10 ? Math.round(hours) : Math.round(hours * 10) / 10;
  return { value: String(rounded), unit: rounded === 1 ? "hr" : "hrs" };
}

/** "since June 2026" style label from an ISO install date. */
export function sinceLabel(iso: string | null): string {
  if (!iso) return "since you installed OpenNeko";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "since you installed OpenNeko";
  return `since ${d.toLocaleDateString("en-IN", { month: "long", year: "numeric" })}`;
}
