import { existsSync } from "node:fs";
import {
  emptyManifest,
  manifestPathFor,
  writeManifest,
} from "@open-neko/plugin-install";

export interface InitOptions {
  repoRoot: string;
}

export async function runInit(
  options: InitOptions,
): Promise<{ created: boolean; path: string }> {
  const file = manifestPathFor(options.repoRoot);
  if (existsSync(file)) {
    return { created: false, path: file };
  }
  await writeManifest(options.repoRoot, emptyManifest());
  return { created: true, path: file };
}
