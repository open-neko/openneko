"use client";

import { useEffect, useState } from "react";
import { FileText, Image as ImageIcon, Sheet, File, ArrowRight } from "lucide-react";
import { useWorkShell } from "../work-shell-context";

// Right-hand context rail for the Ask page (Compact only — CSS hides it in
// Comfortable). Surfaces the answer's headline vitals, generated artifacts, the
// data sources touched, follow-up prompts, and a relevant memory. Vitals and
// follow-ups are channel-agnostic CONTENT the agent emits — the web channel
// renders them as pixels here; another channel could voice them. The rest is
// derived from the run or fetched. Each panel renders only when it has data.

function ext(name: string): string {
  const i = name.lastIndexOf(".");
  return i > 0 ? name.slice(i + 1).toLowerCase() : "";
}

// A terse, honest type label for an artifact — the file extension, or a short
// form of its mime type. Never fabricates size/row counts we don't have.
function artifactMeta(name: string, mime?: string): string {
  const e = ext(name);
  if (e) return e.toUpperCase();
  if (mime) return mime.split("/").pop()?.toUpperCase() ?? mime;
  return "file";
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
  const { railArtifacts, railContext, insertComposerRef } = useWorkShell();
  const { vitals, sources, followups } = railContext;
  const [memory, setMemory] = useState<string | null>(null);

  // A relevant pinned memory, if any — real data from the work memory store.
  useEffect(() => {
    let cancelled = false;
    fetch("/api/work/memories", { cache: "no-store" })
      .then((r) => (r.ok ? r.json() : { memories: [] }))
      .then((d: { memories?: { text: string; pinned?: boolean }[] }) => {
        if (cancelled) return;
        const list = d.memories ?? [];
        const pick = list.find((m) => m.pinned) ?? list[0];
        setMemory(pick?.text ?? null);
      })
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  }, []);

  const isEmpty =
    vitals.length === 0 &&
    railArtifacts.length === 0 &&
    sources.length === 0 &&
    followups.length === 0 &&
    !memory;

  return (
    <aside className="work-rail">
      {isEmpty && (
        <section className="wcr-sect wcr-empty">
          <h4 className="wcr-h">Context</h4>
          <p className="wcr-empty-text">
            Sources touched, artifacts, and follow-ups surface here once the
            agent works through an answer.
          </p>
        </section>
      )}

      {vitals.length > 0 && (
        <section className="wcr-sect">
          <h4 className="wcr-h">Answer vitals</h4>
          <div className="wcr-vitals">
            {vitals.map((v, i) => (
              <div key={i} className="wcr-vital">
                <div className="wcr-vital-k">{v.label}</div>
                <div className="wcr-vital-v">{v.value}</div>
                {v.sub && <div className="wcr-vital-s">{v.sub}</div>}
              </div>
            ))}
          </div>
        </section>
      )}

      {railArtifacts.length > 0 && (
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
                    <span className="block font-mono text-[10.5px] text-text3">{artifactMeta(fileName, a.mimeType)}</span>
                  </span>
                  <span className="ml-auto text-text3 text-sm">↓</span>
                </a>
              );
            })}
          </div>
        </section>
      )}

      {sources.length > 0 && (
        <section className="wcr-sect">
          <h4 className="wcr-h">Sources touched</h4>
          <div className="grid gap-px">
            {sources.map((s, i) => (
              <div key={i} className="wcr-source">
                <span className="wcr-source-dot" aria-hidden="true" />
                <span className="truncate">{s.name}</span>
                {s.detail && <span className="ml-auto font-mono text-[10.5px] text-text3">{s.detail}</span>}
              </div>
            ))}
          </div>
        </section>
      )}

      {followups.length > 0 && (
        <section className="wcr-sect">
          <h4 className="wcr-h">Ask next</h4>
          <div className="grid gap-1.5">
            {followups.map((q) => (
              <button
                key={q}
                type="button"
                onClick={() => insertComposerRef.current?.(q)}
                className="wcr-chip"
              >
                <span className="min-w-0">{q}</span>
                <ArrowRight size={13} strokeWidth={2} className="ml-auto mt-px flex-none opacity-60" />
              </button>
            ))}
          </div>
        </section>
      )}

      {memory && (
        <section className="wcr-sect">
          <h4 className="wcr-h">Related memory</h4>
          <div className="wcr-memo">{memory}</div>
        </section>
      )}
    </aside>
  );
}
