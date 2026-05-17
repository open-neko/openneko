import { existsSync } from "node:fs";
import { manifestPathFor, readManifest } from "@open-neko/plugin-install";
import { checkHost, type HostCheckResult } from "../host-check.js";

export interface DoctorReport {
  host: HostCheckResult;
  manifest: {
    present: boolean;
    path: string;
    pluginCount: number;
  };
}

export async function runDoctor(options: {
  repoRoot: string;
}): Promise<DoctorReport> {
  const manifestPath = manifestPathFor(options.repoRoot);
  const present = existsSync(manifestPath);
  let pluginCount = 0;
  if (present) {
    const manifest = await readManifest(options.repoRoot);
    pluginCount = manifest?.plugins.length ?? 0;
  }
  return {
    host: checkHost(),
    manifest: { present, path: manifestPath, pluginCount },
  };
}
