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
          <div className="library-empty">
            Skill not found.{" "}
            <Link href="/skills" className="library-link">
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
          <div className="library-empty">Loading…</div>
        </div>
        <CreatorCredit />
      </>
    );
  }

  return (
    <>
      <div className="root">
        <AppHeader back={{ href: "/skills", label: "All skills" }} />

        <div className="library-head">
          <div className="library-head-icon">
            <Sparkles size={16} strokeWidth={2} />
          </div>
          <div>
            <div className="library-title">{skill.name}</div>
            {skill.description ? (
              <div className="library-sub">{skill.description}</div>
            ) : null}
          </div>
        </div>

        <section className="library-section">
          <div className="library-section-title">Location</div>
          <div className="library-mono">{skill.path}</div>
        </section>

        <section className="library-section">
          <div className="library-section-title">Files</div>
          <div className="library-files">
            {skill.files.map((file) => (
              <div key={file.path} className="library-file-row">
                <span className="library-file-name">
                  <FileText size={12} strokeWidth={2} />
                  <span>{file.path}</span>
                </span>
                <span className="library-file-size">{file.bytes.toLocaleString()} B</span>
              </div>
            ))}
          </div>
        </section>

        <section className="library-section">
          <div className="library-section-title">SKILL.md</div>
          <div className="library-markdown">
            <ReactMarkdown>{stripFrontmatter(skill.skillMarkdown)}</ReactMarkdown>
          </div>
        </section>
      </div>
      <CreatorCredit />
    </>
  );
}
