"use client";

import { useCallback, useEffect, useState } from "react";
import Link from "next/link";
import { FileText, Sparkles, Trash2 } from "lucide-react";
import AppHeader from "@/components/AppHeader";
import { confirmDialog } from "@/components/ConfirmModal";
import CreatorCredit from "@/components/CreatorCredit";

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
      <div className="root">
        <AppHeader back={{ href: "/work", label: "Back to Work" }} />

        <div className="library-head">
          <div className="library-head-icon">
            <Sparkles size={16} strokeWidth={2} />
          </div>
          <div>
            <div className="library-title">Skills</div>
            <div className="library-sub">
              {loading ? "Loading…" : `${skills.length} installed`}
            </div>
          </div>
        </div>

        {loading ? null : skills.length === 0 ? (
          <div className="library-empty">
            No skills installed. Skills appear here when the agent saves a reusable capability,
            or when one is dropped into the org workspace at <code>skills/</code>.
          </div>
        ) : (
          <ul className="library-list">
            {skills.map((skill) => (
              <li key={skill.name} className="library-item">
                <Link
                  href={`/skills/${encodeURIComponent(skill.name)}`}
                  className="library-item-main library-item-link"
                >
                  <div className="library-item-title">{skill.name}</div>
                  {skill.description ? (
                    <div className="library-item-desc">{skill.description}</div>
                  ) : null}
                  <div className="library-item-meta">
                    <span className="library-meta-pill">
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
                  className="library-icon-btn library-row-action"
                >
                  <Trash2 size={14} strokeWidth={2} />
                </button>
              </li>
            ))}
          </ul>
        )}
      </div>
      <CreatorCredit />
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
