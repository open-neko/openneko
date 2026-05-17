import {
  readManifest,
  removeEntry,
  writeManifest,
} from "@open-neko/plugin-install";

export interface RemoveOptions {
  repoRoot: string;
  name: string;
}

export async function runRemove(
  options: RemoveOptions,
): Promise<{ removed: boolean }> {
  const manifest = await readManifest(options.repoRoot);
  if (!manifest) return { removed: false };
  const updated = removeEntry(manifest, options.name);
  if (updated.plugins.length === manifest.plugins.length) {
    return { removed: false };
  }
  await writeManifest(options.repoRoot, updated);
  return { removed: true };
}
