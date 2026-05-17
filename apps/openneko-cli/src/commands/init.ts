import { existsSync } from "node:fs";
import path from "node:path";
import {
  emptyManifest,
  PLUGIN_MANIFEST_FILE,
  writeManifest,
} from "@open-neko/plugin-install";

export interface InitOptions {
  repoRoot: string;
}

export async function runInit(
  options: InitOptions,
): Promise<{ created: boolean; path: string }> {
  const file = path.join(options.repoRoot, PLUGIN_MANIFEST_FILE);
  if (existsSync(file)) {
    return { created: false, path: file };
  }
  await writeManifest(options.repoRoot, emptyManifest());
  return { created: true, path: file };
}
