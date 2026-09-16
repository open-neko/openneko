import { cp, mkdir, readdir } from "node:fs/promises";
import { dirname, join, relative, sep } from "node:path";
import { rebuildIndexes } from "./tree";

/** Library files a run may read: concept paths and collection prefixes. */
export type AllowedLibrary = { prefixes: readonly string[]; paths: readonly string[] };

function held(allowed: AllowedLibrary, conceptPath: string): boolean {
  return allowed.paths.includes(conceptPath) || allowed.prefixes.some((prefix) => conceptPath.startsWith(prefix));
}

/**
 * Copies only the concept documents the run holds, then rebuilds the index
 * files so they list nothing the run cannot read. Returns the copied paths.
 */
export async function copyAllowedTeamLibrary(
  sourceRoot: string,
  destinationRoot: string,
  allowed: AllowedLibrary,
): Promise<string[]> {
  const copied: string[] = [];
  const visit = async (dir: string): Promise<void> => {
    let entries;
    try {
      entries = await readdir(dir, { withFileTypes: true });
    } catch (error) {
      if ((error as NodeJS.ErrnoException).code === "ENOENT") return;
      throw error;
    }
    for (const entry of entries) {
      const absolute = join(dir, entry.name);
      if (entry.isDirectory()) {
        await visit(absolute);
        continue;
      }
      if (!entry.isFile() || entry.name === "index.md" || entry.name === "log.md") continue;
      const conceptPath = relative(sourceRoot, absolute).split(sep).join("/");
      if (!held(allowed, conceptPath)) continue;
      const target = join(destinationRoot, conceptPath);
      await mkdir(dirname(target), { recursive: true });
      await cp(absolute, target);
      copied.push(conceptPath);
    }
  };
  await visit(sourceRoot);
  if (copied.length > 0) await rebuildIndexes(destinationRoot);
  return copied.sort();
}
