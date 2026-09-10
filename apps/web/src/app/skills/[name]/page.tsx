"use client";

import { use as usePromise, useCallback, useEffect, useState } from "react";
import Link from "next/link";
import { ArrowLeft, FileText } from "lucide-react";
import ReactMarkdown from "react-markdown";
import PageHeading from "@/components/PageHeading";
import { Button, buttonClassName } from "@/components/ui/button";

function stripFrontmatter(markdown: string): string {
  return markdown.replace(/^---\n[\s\S]*?\n---\n?/, "").trimStart();
}

type SkillDetail = {
  name: string;
  description: string;
  fileCount: number;
  updatedAt: string;
  skillMarkdown: string;
  files: Array<{ path: string; bytes: number }>;
};

type PageProps = {
  params: Promise<{ name: string }>;
};

async function fetchSkill(
  name: string,
  signal?: AbortSignal,
): Promise<SkillDetail | null> {
  const response = await fetch(`/api/work/skills/${encodeURIComponent(name)}`, {
    cache: "no-store",
    signal,
  });
  if (response.status === 404) return null;
  if (!response.ok) throw new Error("Skill could not be loaded.");
  const data = (await response.json()) as { skill: SkillDetail };
  return data.skill;
}

export default function SkillDetailPage({ params }: PageProps) {
  const { name } = usePromise(params);
  const [skill, setSkill] = useState<SkillDetail | null>(null);
  const [state, setState] = useState<"loading" | "ready" | "missing" | "error">(
    "loading",
  );

  const load = useCallback(async () => {
    setState("loading");
    try {
      const nextSkill = await fetchSkill(name);
      if (!nextSkill) {
        setState("missing");
        return;
      }
      setSkill(nextSkill);
      setState("ready");
    } catch {
      setState("error");
    }
  }, [name]);

  useEffect(() => {
    const controller = new AbortController();
    void fetchSkill(name, controller.signal)
      .then((nextSkill) => {
        if (!nextSkill) {
          setState("missing");
          return;
        }
        setSkill(nextSkill);
        setState("ready");
      })
      .catch(() => {
        if (!controller.signal.aborted) setState("error");
      });
    return () => controller.abort();
  }, [name]);

  if (state !== "ready" || !skill) {
    return (
      <div className="library-page skill-detail-page">
        <PageHeading
          eyebrow={
            <Link href="/skills" className="skill-back-link">
              <ArrowLeft aria-hidden="true" strokeWidth={2} />
              Skills
            </Link>
          }
          title={state === "loading" ? "Loading skill" : "Skill unavailable"}
        />
        <main className="library-main">
          {state === "loading" ? (
            <div className="library-loading" role="status" aria-label="Loading skill">
              <span />
              <span />
              <span />
            </div>
          ) : (
            <div className="library-error" role="alert">
              <div>
                <strong>{state === "missing" ? "Skill not found" : "Load failed"}</strong>
                <span>
                  {state === "missing"
                    ? "This skill is no longer in the organization workspace."
                    : "OpenNeko could not read this skill."}
                </span>
              </div>
              {state === "error" ? (
                <Button
                  variant="danger"
                  size="sm"
                  className="shrink-0"
                  onClick={() => void load()}
                >
                  Retry
                </Button>
              ) : (
                <Link href="/skills" className={buttonClassName({ size: "sm" })}>
                  All skills
                </Link>
              )}
            </div>
          )}
        </main>
      </div>
    );
  }

  return (
    <div className="library-page skill-detail-page">
      <PageHeading
        eyebrow={
          <Link href="/skills" className="skill-back-link">
            <ArrowLeft aria-hidden="true" strokeWidth={2} />
            Skills
          </Link>
        }
        title={skill.name}
        actions={
          <div className="library-head-stats" aria-label="Skill details">
            <div>
              <strong>{String(skill.fileCount).padStart(2, "0")}</strong>
              <span>{skill.fileCount === 1 ? "file" : "files"}</span>
            </div>
            <div>
              <strong>{formatShortDate(skill.updatedAt)}</strong>
              <span>updated</span>
            </div>
          </div>
        }
      />

      <main className="skill-detail-workspace">
        <aside className="skill-manifest">
          <section>
            <span className="skill-detail-label">Files</span>
            <ol>
              {skill.files.map((file, index) => (
                <li key={file.path}>
                  <span className="library-index">
                    {String(index + 1).padStart(2, "0")}
                  </span>
                  <FileText aria-hidden="true" strokeWidth={1.9} />
                  <span>{file.path}</span>
                  <small>{formatBytes(file.bytes)}</small>
                </li>
              ))}
            </ol>
          </section>
        </aside>

        <article className="skill-instructions">
          {skill.description ? (
            <p className="skill-detail-summary">{skill.description}</p>
          ) : null}
          <header>
            <span>Primary instruction</span>
            <h2>SKILL.md</h2>
          </header>
          <div className="library-markdown">
            <ReactMarkdown>{stripFrontmatter(skill.skillMarkdown)}</ReactMarkdown>
          </div>
        </article>
      </main>
    </div>
  );
}

function formatShortDate(value: string): string {
  return new Date(value)
    .toLocaleDateString("en-IN", { month: "short", day: "numeric" })
    .toUpperCase();
}

function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  return `${(bytes / 1024).toFixed(bytes < 10_240 ? 1 : 0)} KB`;
}
