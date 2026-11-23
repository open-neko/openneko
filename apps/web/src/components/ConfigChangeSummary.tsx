import { Disclosure } from "@/components/ui/disclosure";
import { diffLines } from "@/lib/action-changes";
import { cn } from "@/lib/cn";
import { configChangeItems, type ConfigChangeSummary as Summary } from "@/lib/config-summary";

function Sentences({ lines, empty }: { lines: string[] | null; empty: string }) {
  if (!lines || lines.length === 0) {
    return <p className="m-0 text-ui-body-sm text-text3">{empty}</p>;
  }
  return (
    <ul className="m-0 grid list-none gap-1 p-0 text-ui-body-sm leading-[1.6] text-text">
      {lines.map((line, index) => (
        <li key={index} className="break-words">
          {line}
        </li>
      ))}
    </ul>
  );
}

export function ConfigChangeSummary({
  summary,
  compact = false,
  className,
}: {
  summary: Summary;
  compact?: boolean;
  className?: string;
}) {
  const items = configChangeItems(summary);
  if (items.length === 0) return null;
  const lines = diffLines(summary.yaml.before, summary.yaml.after);
  return (
    <div className={cn("grid gap-3", className)} data-slot="config-change-summary">
      {items.map((item) => (
        <section
          key={item.key}
          aria-label={`Changes to ${item.label}`}
          className="overflow-hidden rounded-inner border border-border bg-card"
        >
          <h3 className="m-0 border-b border-border px-3.5 py-2 font-display text-ui-caption font-semibold text-text2">
            {item.label}
          </h3>
          <div className={cn("grid gap-3 px-3.5 py-3", !compact && "md:grid-cols-2")}>
            <div className="grid min-w-0 gap-1.5">
              <span className="text-ui-caption font-semibold text-text3">Now</span>
              <Sentences lines={item.before} empty="Not set up yet." />
            </div>
            <div className="grid min-w-0 gap-1.5">
              <span className="text-ui-caption font-semibold text-text3">After approval</span>
              <Sentences lines={item.after} empty="Removed." />
            </div>
          </div>
        </section>
      ))}
      {compact || lines.length === 0 ? null : (
        <Disclosure title="Show the YAML change">
          <pre className="m-0 overflow-x-auto font-mono text-ui-caption leading-[1.6]">
            {lines.map((line, index) => (
              <div
                key={index}
                className={cn(
                  "whitespace-pre px-1",
                  line.kind === "added" && "bg-success-soft text-success-ink",
                  line.kind === "removed" && "bg-danger-soft text-danger",
                  line.kind === "same" && "text-text2",
                )}
              >
                <span aria-hidden="true">
                  {line.kind === "added" ? "+ " : line.kind === "removed" ? "- " : "  "}
                </span>
                <span className="sr-only">
                  {line.kind === "added" ? "Added: " : line.kind === "removed" ? "Removed: " : ""}
                </span>
                {line.text}
              </div>
            ))}
          </pre>
        </Disclosure>
      )}
    </div>
  );
}
