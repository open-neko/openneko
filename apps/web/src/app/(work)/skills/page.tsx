"use client";

import { useCallback, useEffect, useState } from "react";
import Link from "next/link";
import { FileText, Sparkles, Trash2 } from "lucide-react";
import { confirmDialog } from "@/components/ConfirmModal";

type SkillSummary = {
  name: string;
  description: string;
  fileCount: number;
  updatedAt: string;
};

export default function SkillsPage() {
  const [skills, setSkills] = useState<SkillSummary[]>([]);
  const [loading, setLoading] = useState(true);
  const [busyName, setBusyName] = useState<string | null>(null);

  const refresh = useCallback(async () => {
    setLoading(true);
    try {
      const res = await fetch("/api/work/skills", { cache: "no-store" });
      const data = (await res.json()) as { skills: SkillSummary[] };
      setSkills(data.skills ?? []);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  const remove = useCallback(
    async (skillName: string) => {
      const ok = await confirmDialog({
        title: `Delete skill "${skillName}"?`,
        description: "Removes the skill folder from the org workspace.",
        confirmLabel: "Delete",
        destructive: true,
      });
      if (!ok) return;
      setBusyName(skillName);
      try {
        const res = await fetch(`/api/work/skills/${encodeURIComponent(skillName)}`, { method: "DELETE" });
        if (res.ok) await refresh();
      } finally {
        setBusyName(null);
      }
    },
    [refresh],
  );

  return (
    <>
      <div className="flex items-center gap-3 mb-6">
        <div className="w-9 h-9 rounded-xl bg-accent-soft text-accent inline-flex items-center justify-center shrink-0">
          <Sparkles size={16} strokeWidth={2} />
        </div>
        <div>
          <div className="font-display text-2xl font-bold leading-[1.1] text-text">Skills</div>
          <div className="text-[13px] text-text3 mt-0.5">
            {loading ? "Loading…" : `${skills.length} installed`}
          </div>
        </div>
      </div>

      {loading ? null : skills.length === 0 ? (
        <div className="bg-card border border-dashed border-border rounded-2xl px-[22px] py-5 text-[13.5px] leading-[1.55] text-text3">
          No skills installed. Skills appear here when the agent saves a reusable capability,
          or when one is dropped into the org workspace at <code className="font-mono text-xs bg-black/5 px-1.5 py-px rounded">skills/</code>.
        </div>
      ) : (
        <ul className="list-none p-0 m-0 flex flex-col gap-2">
          {skills.map((skill) => (
            <li
              key={skill.name}
              className="group relative flex items-start gap-2 p-1 bg-card border border-border rounded-2xl text-inherit list-none transition hover:border-accent hover:shadow-soft hover:-translate-y-px"
            >
              <Link
                href={`/skills/${encodeURIComponent(skill.name)}`}
                className="flex-1 min-w-0 flex flex-col gap-1 px-3 py-2.5 rounded-[10px] no-underline text-inherit cursor-pointer"
              >
                <div className="text-sm font-semibold text-text">{skill.name}</div>
                {skill.description ? (
                  <div className="text-[13px] leading-[1.5] text-text2 line-clamp-2">{skill.description}</div>
                ) : null}
                <div className="flex flex-wrap items-center gap-2.5 text-[11.5px] text-text3">
                  <span className="inline-flex items-center gap-1 bg-black/5 text-text2 px-2 py-0.5 rounded-full text-[11px] font-medium">
                    <FileText size={11} strokeWidth={2} />
                    {skill.fileCount} {skill.fileCount === 1 ? "file" : "files"}
                  </span>
                  <span>updated {formatDate(skill.updatedAt)}</span>
                </div>
              </Link>
              <button
                type="button"
                disabled={busyName === skill.name}
                onClick={() => void remove(skill.name)}
                aria-label={`Delete skill ${skill.name}`}
                title="Delete skill"
                className="mt-1.5 mr-1.5 w-9 h-9 rounded-[9px] bg-transparent border-0 text-text3 inline-flex items-center justify-center transition opacity-0 pointer-events-none cursor-pointer hover:bg-[rgba(220,53,69,0.1)] hover:text-[var(--danger-hover)] disabled:opacity-50 disabled:cursor-not-allowed group-hover:opacity-100 group-hover:pointer-events-auto group-focus-within:opacity-100 group-focus-within:pointer-events-auto [@media(hover:none)]:opacity-100 [@media(hover:none)]:pointer-events-auto"
              >
                <Trash2 size={14} strokeWidth={2} />
              </button>
            </li>
          ))}
        </ul>
      )}
    </>
  );
}

function formatDate(value: string): string {
  return new Date(value).toLocaleDateString("en-IN", {
    month: "short",
    day: "numeric",
    year: "numeric",
  });
}
