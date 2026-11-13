"use client";

import Link from "next/link";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useRouter } from "next/navigation";
import { toast } from "sonner";
import { ArrowUpRight, Copy, FileText, Share2, Trash2, Upload } from "lucide-react";
import { confirmDialog } from "@/components/ConfirmModal";
import { SkillCopyDialog } from "@/components/SkillCopyDialog";
import { Badge } from "@/components/ui/badge";
import { shareSkill, type SkillScope } from "@/lib/skill-actions";
import PageHeading from "@/components/PageHeading";
import { Button } from "@/components/ui/button";
import { MenuItem, OverflowMenu } from "@/components/ui/overflow-menu";
import { EmptyState } from "@/components/ui/empty";
import { SearchInput } from "@/components/ui/search-input";
import { Input } from "@/components/ui/input";
import { matchesListSearch } from "@/lib/list-search";

type SkillSummary = {
  name: string;
  description: string;
  fileCount: number;
  updatedAt: string;
  scope: SkillScope;
  canEdit: boolean;
  canShare: boolean;
};

async function fetchSkills(signal?: AbortSignal): Promise<{ skills: SkillSummary[]; canImport: boolean; canCopy: boolean }> {
  const response = await fetch("/api/v1/work/skills", {
    cache: "no-store",
    signal,
  });
  if (!response.ok) throw new Error("Skills could not be loaded.");
  const data = (await response.json()) as { skills?: SkillSummary[]; canImport?: boolean; canCopy?: boolean };
  return { skills: data.skills ?? [], canImport: data.canImport === true, canCopy: data.canCopy === true };
}

export default function SkillsPage() {
  const router = useRouter();
  const archiveInput = useRef<HTMLInputElement>(null);
  const [skills, setSkills] = useState<SkillSummary[]>([]);
  const [canImport, setCanImport] = useState(false);
  const [canCopy, setCanCopy] = useState(false);
  const [copySource, setCopySource] = useState<string | null>(null);
  const [importing, setImporting] = useState(false);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [busyName, setBusyName] = useState<string | null>(null);
  const [query, setQuery] = useState("");

  const refresh = useCallback(async () => {
    setLoading(true);
    try {
      const data = await fetchSkills();
      setSkills(data.skills);
      setCanImport(data.canImport);
      setCanCopy(data.canCopy);
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
        setSkills(data.skills);
        setCanImport(data.canImport);
        setCanCopy(data.canCopy);
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

  const importArchive = useCallback(async (file: File | undefined) => {
    if (!file) return;
    if (file.size > 16 * 1024 * 1024) {
      toast.error("Skill archive must be at most 16 MB.");
      if (archiveInput.current) archiveInput.current.value = "";
      return;
    }
    setImporting(true);
    try {
      const form = new FormData();
      form.set("file", file);
      const response = await fetch("/api/v1/work/skills", { method: "POST", body: form });
      const data = await response.json() as { name?: string; error?: string };
      if (!response.ok || !data.name) throw new Error(data.error ?? "Skill could not be imported.");
      toast.success(`Imported ${data.name}.`);
      router.push(`/skills/${encodeURIComponent(data.name)}`);
    } catch (cause) {
      toast.error(cause instanceof Error ? cause.message : "Skill could not be imported.");
    } finally {
      setImporting(false);
      if (archiveInput.current) archiveInput.current.value = "";
    }
  }, [router]);

  const share = useCallback(
    async (skillName: string) => {
      const ok = await confirmDialog({
        title: `Share "${skillName}" with the team?`,
        description: "The skill moves to the team skills. Members see it when their groups hold it.",
        confirmLabel: "Share",
      });
      if (!ok) return;
      setBusyName(skillName);
      try {
        await shareSkill(skillName);
        toast.success(`${skillName} is now a team skill.`);
        await refresh();
      } catch (cause) {
        toast.error(cause instanceof Error ? cause.message : "The skill could not be shared.");
      } finally {
        setBusyName(null);
      }
    },
    [refresh],
  );

  const remove = useCallback(
    async (skill: SkillSummary) => {
      const skillName = skill.name;
      const ok = await confirmDialog({
        title: `Delete skill "${skillName}"?`,
        description: skill.scope === "own"
          ? "This removes your own skill. Team skills stay as they are."
          : "This removes the team skill for everyone in the organization.",
        confirmLabel: "Delete",
        destructive: true,
      });
      if (!ok) return;
      setBusyName(skillName);
      try {
        const response = await fetch(
          `/api/v1/work/skills/${encodeURIComponent(skillName)}`,
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
          <div className="flex flex-wrap items-center gap-4">
            <div className="library-head-stats" aria-label="Skill inventory">
              <div>
                <strong>{skills.length}</strong>
                <span>installed</span>
              </div>
              <div>
                <strong>{totalFiles}</strong>
                <span>files</span>
              </div>
            </div>
            {canImport ? (
              <Button variant="primary" disabled={importing} onClick={() => archiveInput.current?.click()}>
                <Upload aria-hidden="true" />{importing ? "Importing…" : "Import skill"}
              </Button>
            ) : null}
          </div>
        }
      />

      <Input
        ref={archiveInput}
        type="file"
        accept=".skill,.zip,application/zip"
        hidden
        aria-label="Choose a skill archive"
        onChange={(event) => void importArchive(event.target.files?.[0])}
      />

      <main className="library-main">
        <SearchInput
          label="Search skills"
          value={query}
          onChange={(event) => setQuery(event.target.value)}
          placeholder="Search installed skills"
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
              <span>Installed skills</span>
              <h2>Installed skills</h2>
            </div>
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
                  : "Skills appear here when OpenNeko saves a reusable capability or an administrator imports one."
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
                        {index + 1}
                      </span>
                      <strong>
                        {skill.name}
                        {skill.scope === "own" ? (
                          <Badge variant="success" className="ml-2 align-middle">Yours</Badge>
                        ) : null}
                      </strong>
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
                    {canCopy || skill.canEdit ? (
                      <OverflowMenu
                        label={`Actions for ${skill.name}`}
                        className="skill-delete-control"
                      >
                        {canCopy ? (
                          <MenuItem disabled={busyName === skill.name} onSelect={() => setCopySource(skill.name)}>
                            <Copy aria-hidden="true" strokeWidth={1.9} />
                            Copy skill
                          </MenuItem>
                        ) : null}
                        {skill.canShare ? (
                          <MenuItem disabled={busyName === skill.name} onSelect={() => void share(skill.name)}>
                            <Share2 aria-hidden="true" strokeWidth={1.9} />
                            Share with the team
                          </MenuItem>
                        ) : null}
                        {skill.canEdit ? (
                          <MenuItem danger disabled={busyName === skill.name} onSelect={() => void remove(skill)}>
                            <Trash2 aria-hidden="true" strokeWidth={1.9} />
                            Delete skill
                          </MenuItem>
                        ) : null}
                      </OverflowMenu>
                    ) : null}
                  </li>
                ))}
              </ol>
            </>
          )}
        </section>
        {copySource ? (
          <SkillCopyDialog
            source={copySource}
            onClose={() => setCopySource(null)}
            onCopied={(name) => {
              setCopySource(null);
              toast.success(`Copied to ${name}.`);
              router.push(`/skills/${encodeURIComponent(name)}`);
            }}
          />
        ) : null}
        {canImport ? (
          <p className="text-ui-caption text-text3">
            Import a .skill or ZIP archive that holds one skill directory with SKILL.md and its supporting files.
          </p>
        ) : null}
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
