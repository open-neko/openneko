"use client";

import Link from "next/link";
import { FileText, Image as ImageIcon, Sheet, File, ArrowRight } from "lucide-react";
import { useWorkShell } from "../work-shell-context";

// Right-hand context rail for the Ask page (Compact only — CSS hides it in
// Comfortable). Surfaces the artifacts the agent produced this thread plus
// quick follow-up prompts, so the answer's outputs stay reachable instead of
// scrolling away. Hidden entirely when there's nothing to show.

const ASK_NEXT = [
  "Break this down by region",
  "Compare to last quarter",
  "Which of these are at risk?",
];

function ext(name: string): string {
  const i = name.lastIndexOf(".");
  return i > 0 ? name.slice(i + 1).toLowerCase() : "";
}

function ArtifactIcon({ name, mime }: { name: string; mime?: string }) {
  const e = ext(name);
  if (e === "csv" || e === "tsv" || e === "xlsx" || mime?.includes("spreadsheet")) {
    return <span className="wcr-ic sheet"><Sheet size={15} strokeWidth={2} /></span>;
  }
  if (["png", "jpg", "jpeg", "gif", "webp", "svg"].includes(e) || mime?.startsWith("image/")) {
    return <span className="wcr-ic image"><ImageIcon size={15} strokeWidth={2} /></span>;
  }
  if (e === "pdf" || e === "docx" || e === "md" || e === "txt" || e === "html" || e === "pptx") {
    return <span className="wcr-ic doc"><FileText size={15} strokeWidth={2} /></span>;
  }
  return <span className="wcr-ic file"><File size={15} strokeWidth={2} /></span>;
}

export default function WorkContextRail() {
  const { railArtifacts } = useWorkShell();
  const hasArtifacts = railArtifacts.length > 0;

  return (
    <aside className="work-rail">
      {hasArtifacts && (
        <section className="wcr-sect">
          <h4 className="wcr-h">Artifacts</h4>
          <div className="grid gap-1.5">
            {railArtifacts.map((a, i) => {
              const fileName = a.label || a.path.split("/").slice(-1)[0];
              return (
                <a
                  key={`${a.path}-${i}`}
                  href={`/api/work/files/${a.path.replace(/^.*\/(runs|uploads|skills|memory)\//, "$1/")}`}
                  className="wcr-artifact"
                  title={a.path}
                >
                  <ArtifactIcon name={fileName} mime={a.mimeType} />
                  <span className="min-w-0">
                    <span className="block text-[12.5px] font-medium text-text truncate">{fileName}</span>
                    {a.mimeType && <span className="block font-mono text-[10.5px] text-text3">{a.mimeType}</span>}
                  </span>
                  <span className="ml-auto text-text3 text-sm">↓</span>
                </a>
              );
            })}
          </div>
        </section>
      )}

      <section className="wcr-sect">
        <h4 className="wcr-h">Ask next</h4>
        <div className="grid gap-1.5">
          {ASK_NEXT.map((q) => (
            <Link key={q} href={`/work?seed=${encodeURIComponent(q)}`} className="wcr-chip">
              <span className="min-w-0 truncate">{q}</span>
              <ArrowRight size={13} strokeWidth={2} className="ml-auto flex-none opacity-60" />
            </Link>
          ))}
        </div>
      </section>
    </aside>
  );
}
