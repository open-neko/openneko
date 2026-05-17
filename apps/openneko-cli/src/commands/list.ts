import { readManifest, type ManifestEntry } from "@open-neko/plugin-install";

export interface ListOptions {
  repoRoot: string;
}

export async function runList(
  options: ListOptions,
): Promise<{ entries: ManifestEntry[]; hadManifest: boolean }> {
  const manifest = await readManifest(options.repoRoot);
  if (!manifest) return { entries: [], hadManifest: false };
  return { entries: manifest.plugins, hadManifest: true };
}
