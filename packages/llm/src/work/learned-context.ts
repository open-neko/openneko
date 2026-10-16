import { readdir, readFile } from "node:fs/promises";
import { join } from "node:path";

export const LEARNED_DIR = "learned";
export const SOURCE_NOTES_FILE = "NOTES.md";
export const ORG_PLAYBOOK_FILE = "PLAYBOOK.md";
export const MAX_ORG_RULES = 8;

export type LearnedContext = {
  /** Applied data notes for every source, one block per source. */
  sourceNotes: string;
  /** Applied org playbook rules, at most MAX_ORG_RULES lines. */
  orgPlaybook: string;
};

async function appliedBody(path: string): Promise<{ body: string; label: string } | null> {
  try {
    const raw = await readFile(path, "utf8");
    const match = raw.match(/^---\s*\n([\s\S]*?)\n---\s*\n?([\s\S]*)$/);
    if (!match || !/^status:\s*applied\s*$/m.test(match[1] ?? "")) return null;
    const body = (match[2] ?? "").trim();
    if (!body) return null;
    const label = /^target_id:\s*(.+)$/m.exec(match[1] ?? "")?.[1]?.trim() ?? "source";
    return { body, label };
  } catch {
    return null;
  }
}

/** Reads what the learner wrote for the prompt; missing files mean nothing learned. */
export async function readLearnedContext(orgRoot: string): Promise<LearnedContext> {
  const sourcesRoot = join(orgRoot, LEARNED_DIR, "sources");
  let sourceIds: string[] = [];
  try {
    sourceIds = (await readdir(sourcesRoot, { withFileTypes: true }))
      .filter((entry) => entry.isDirectory())
      .map((entry) => entry.name)
      .sort();
  } catch {
    sourceIds = [];
  }
  const applied = (
    await Promise.all(sourceIds.map((id) => appliedBody(join(sourcesRoot, id, SOURCE_NOTES_FILE))))
  ).filter((notes): notes is { body: string; label: string } => notes !== null);
  const notes = applied.map((entry) =>
    applied.length > 1 ? `Source ${entry.label}:\n${entry.body}` : entry.body,
  );
  const playbook = await appliedBody(join(orgRoot, LEARNED_DIR, "org", ORG_PLAYBOOK_FILE));
  return {
    sourceNotes: notes.join("\n\n"),
    orgPlaybook: playbook
      ? playbook.body
          .split("\n")
          .filter((line) => line.startsWith("- "))
          .slice(0, MAX_ORG_RULES)
          .join("\n")
      : "",
  };
}
