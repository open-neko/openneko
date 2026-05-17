"use client";

import { use as usePromise, useCallback, useEffect, useState } from "react";
import Link from "next/link";
import { FileText, Sparkles } from "lucide-react";
import ReactMarkdown from "react-markdown";
import AppHeader from "@/components/AppHeader";
import CreatorCredit from "@/components/CreatorCredit";

function stripFrontmatter(markdown: string): string {
  return markdown.replace(/^---\n[\s\S]*?\n---\n?/, "").trimStart();
}

type SkillDetail = {
  name: string;
  description: string;
  fileCount: number;
  updatedAt: string;
  path: string;
  skillMarkdown: string;
  files: Array<{ path: string; bytes: number }>;
};

type PageProps = {
  params: Promise<{ name: string }>;
};

export default function SkillDetailPage({ params }: PageProps) {
  const { name } = usePromise(params);
  const [skill, setSkill] = useState<SkillDetail | null>(null);
  const [notFound, setNotFound] = useState(false);

  const load = useCallback(async () => {
    const res = await fetch(`/api/work/skills/${encodeURIComponent(name)}`, { cache: "no-store" });
    if (!res.ok) {
      setNotFound(true);
      return;
    }
    const data = (await res.json()) as { skill: SkillDetail };
    setSkill(data.skill);
  }, [name]);

  useEffect(() => {
    void load();
  }, [load]);

  if (notFound) {
    return (
      <>
        <div className="root">
          <AppHeader back={{ href: "/skills", label: "All skills" }} />
          <div className="bg-card border border-dashed border-border rounded-2xl px-[22px] py-5 text-[13.5px] leading-[1.55] text-text3">
            Skill not found.{" "}
            <Link href="/skills" className="text-accent no-underline hover:underline">
              Back to skills
            </Link>
            .
          </div>
        </div>
        <CreatorCredit />
      </>
    );
  }

  if (!skill) {
    return (
      <>
        <div className="root">
          <AppHeader back={{ href: "/skills", label: "All skills" }} />
          <div className="bg-card border border-dashed border-border rounded-2xl px-[22px] py-5 text-[13.5px] leading-[1.55] text-text3">Loading…</div>
        </div>
        <CreatorCredit />
      </>
    );
  }

  return (
    <>
      <div className="root">
        <AppHeader back={{ href: "/skills", label: "All skills" }} />

        <div className="flex items-center gap-3 mb-6">
          <div className="w-9 h-9 rounded-xl bg-accent-soft text-accent inline-flex items-center justify-center shrink-0">
            <Sparkles size={16} strokeWidth={2} />
          </div>
          <div>
            <div className="font-display text-2xl font-bold leading-[1.1] text-text">{skill.name}</div>
            {skill.description ? (
              <div className="text-[13px] text-text3 mt-0.5">{skill.description}</div>
            ) : null}
          </div>
        </div>

        <section className="mt-7">
          <div className="text-[11px] font-semibold uppercase tracking-[0.1em] text-text3 mb-2.5">Location</div>
          <div className="font-mono text-xs text-text2 bg-card border border-border rounded-xl px-3 py-2.5 break-all">{skill.path}</div>
        </section>

        <section className="mt-7">
          <div className="text-[11px] font-semibold uppercase tracking-[0.1em] text-text3 mb-2.5">Files</div>
          <div className="bg-card border border-border rounded-xl overflow-hidden">
            {skill.files.map((file) => (
              <div
                key={file.path}
                className="flex items-center justify-between gap-3 px-3 py-2 text-[12.5px] border-t border-border first:border-t-0"
              >
                <span className="inline-flex items-center gap-2 text-text font-mono text-xs min-w-0">
                  <FileText size={12} strokeWidth={2} className="text-text3 shrink-0" />
                  <span className="whitespace-nowrap overflow-hidden text-ellipsis">{file.path}</span>
                </span>
                <span className="font-mono text-[11.5px] text-text3 tabular-nums shrink-0">{file.bytes.toLocaleString()} B</span>
              </div>
            ))}
          </div>
        </section>

        <section className="mt-7">
          <div className="text-[11px] font-semibold uppercase tracking-[0.1em] text-text3 mb-2.5">SKILL.md</div>
          <div className="library-markdown">
            <ReactMarkdown>{stripFrontmatter(skill.skillMarkdown)}</ReactMarkdown>
          </div>
        </section>
      </div>
      <CreatorCredit />
    </>
  );
}
