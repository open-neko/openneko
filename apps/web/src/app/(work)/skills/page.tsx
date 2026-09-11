"use client";

import Link from "next/link";
import { useCallback, useEffect, useMemo, useState } from "react";
import { ArrowUpRight, FileText, Trash2 } from "lucide-react";
import { confirmDialog } from "@/components/ConfirmModal";
import PageHeading from "@/components/PageHeading";
import { Button, IconButton } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty";
import { SearchInput } from "@/components/ui/search-input";
import { matchesListSearch } from "@/lib/list-search";

type SkillSummary = {
  name: string;
  description: string;
  fileCount: number;
  updatedAt: string;
};

async function fetchSkills(signal?: AbortSignal): Promise<SkillSummary[]> {
  const response = await fetch("/api/work/skills", {
    cache: "no-store",
    signal,
  });
  if (!response.ok) throw new Error("Skills could not be loaded.");
  const data = (await response.json()) as { skills?: SkillSummary[] };
  return data.skills ?? [];
}

export default function SkillsPage() {
  const [skills, setSkills] = useState<SkillSummary[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [busyName, setBusyName] = useState<string | null>(null);
  const [query, setQuery] = useState("");

  const refresh = useCallback(async () => {
    setLoading(true);
    try {
      setSkills(await fetchSkills());
      setError(null);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Skills could not be loaded.");
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    const controller = new AbortController();
    void fetchSkills(controller.signal)
      .then((data) => {
        setSkills(data);
        setError(null);
      })
      .catch((cause: unknown) => {
        if (controller.signal.aborted) return;
        setError(cause instanceof Error ? cause.message : "Skills could not be loaded.");
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => controller.abort();
  }, []);

  const remove = useCallback(
    async (skillName: string) => {
      const ok = await confirmDialog({
        title: `Delete skill "${skillName}"?`,
        description: "This removes the skill folder from the organization workspace.",
        confirmLabel: "Delete",
        destructive: true,
      });
      if (!ok) return;
      setBusyName(skillName);
      try {
        const response = await fetch(
          `/api/work/skills/${encodeURIComponent(skillName)}`,
          { method: "DELETE" },
        );
        if (!response.ok) throw new Error("Skill could not be deleted.");
        await refresh();
      } catch (cause) {
        setError(
          cause instanceof Error ? cause.message : "Skill could not be deleted.",
        );
      } finally {
        setBusyName(null);
      }
    },
    [refresh],
  );

  const totalFiles = useMemo(
    () => skills.reduce((total, skill) => total + skill.fileCount, 0),
    [skills],
  );
  const visibleSkills = useMemo(
    () =>
      skills.filter((skill) =>
        matchesListSearch(query, skill.name, skill.description),
      ),
    [query, skills],
  );

  return (
    <div className="library-page skills-library">
      <PageHeading
        title="Skills"
        description="Capabilities your agents can call while they work a run."
        actions={
          <div className="library-head-stats" aria-label="Skill inventory">
            <div>
              <strong>{String(skills.length).padStart(2, "0")}</strong>
              <span>installed</span>
            </div>
            <div>
              <strong>{String(totalFiles).padStart(2, "0")}</strong>
              <span>files</span>
            </div>
          </div>
        }
      />

      <main className="library-main">
        <SearchInput
          label="Search skills"
          value={query}
          onChange={(event) => setQuery(event.target.value)}
          placeholder="Search installed skills"
          className="bg-card max-w-[520px]"
        />

        {error ? (
          <div className="library-error" role="alert">
            <div>
              <strong>Skills unavailable</strong>
              <span>{error}</span>
            </div>
            <Button
              variant="danger"
              size="sm"
              className="shrink-0"
              onClick={() => void refresh()}
            >
              Retry
            </Button>
          </div>
        ) : null}

        <section className="library-section">
          <header className="library-section-head">
            <div>
              <span>Runtime inventory</span>
              <h2>Installed skills</h2>
            </div>
            <strong>{String(visibleSkills.length).padStart(2, "0")}</strong>
          </header>

          {loading ? (
            <div className="library-loading" role="status" aria-label="Loading skills">
              <span />
              <span />
              <span />
            </div>
          ) : visibleSkills.length === 0 ? (
            <EmptyState
              className="library-empty"
              title={query ? "No matching skills" : "No installed skills"}
              body={
                query
                  ? "Try another skill name or trigger phrase."
                  : "Skills appear here when OpenNeko saves a reusable capability or one is installed into the organization workspace."
              }
            />
          ) : (
            <>
              <div className="skills-table-head" aria-hidden="true">
                <span />
                <span>Skill</span>
                <span>When it is used</span>
                <span>Files</span>
                <span>Updated</span>
                <span />
              </div>
              <ol className="skills-index">
                {visibleSkills.map((skill, index) => (
                  <li key={skill.name} className="skill-index-row">
                    <Link
                      href={`/skills/${encodeURIComponent(skill.name)}`}
                      className="skill-index-link"
                    >
                      <span className="library-index">
                        {String(index + 1).padStart(2, "0")}
                      </span>
                      <strong>{skill.name}</strong>
                      <p>{skill.description || "No trigger description provided."}</p>
                      <span className="skill-file-count">
                        <FileText aria-hidden="true" strokeWidth={1.9} />
                        {skill.fileCount}
                      </span>
                      <span className="skill-updated">{formatDate(skill.updatedAt)}</span>
                      <ArrowUpRight
                        className="skill-row-arrow"
                        aria-hidden="true"
                        strokeWidth={1.9}
                      />
                    </Link>
                    <IconButton
                      label={`Delete skill ${skill.name}`}
                      variant="danger"
                      className="skill-delete-control"
                      disabled={busyName === skill.name}
                      onClick={() => void remove(skill.name)}
                    >
                      <Trash2 aria-hidden="true" strokeWidth={1.9} />
                    </IconButton>
                  </li>
                ))}
              </ol>
            </>
          )}
        </section>
      </main>
    </div>
  );
}

function formatDate(value: string): string {
  return new Date(value).toLocaleDateString("en-IN", {
    month: "short",
    day: "numeric",
    year: "2-digit",
  });
}
