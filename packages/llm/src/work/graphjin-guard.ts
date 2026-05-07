import { writeFile } from "node:fs/promises";
import { constants as fsConstants } from "node:fs";
import { access } from "node:fs/promises";
import { join } from "node:path";

const BLOCKED_PATTERNS = [
  /\bmutation\b/i,
  /\bsubscription\b/i,
  /\bserve\b/i,
  /\bconfig\b/i,
  /\bmigrate\b/i,
  /\bsecrets?\b/i,
  /\badmin\b/i,
  /\bnew\b/i,
];

export function isGraphjinCommandSafe(args: string[]): boolean {
  const joined = args.join(" ");
  return !BLOCKED_PATTERNS.some((pattern) => pattern.test(joined));
}

export async function ensureGraphjinGuard(
  binRoot: string,
  graphjinBinary: string,
): Promise<string> {
  const wrapperPath = join(binRoot, "graphjin");
  const script = [
    "#!/usr/bin/env bash",
    "set -euo pipefail",
    "joined=\"$*\"",
    "case \"$joined\" in",
    "  *mutation*|*Mutation*|*subscription*|*Subscription*|* serve *|serve\\ *|* config *|config\\ *|* migrate *|migrate\\ *|* secret*|secret*|* admin *|admin\\ *|* new *|new\\ *)",
    "    echo \"Neko Work blocks GraphJin mutations and server-changing commands. Read/query only.\" >&2",
    "    exit 2",
    "    ;;",
    "esac",
    `exec "${graphjinBinary}" "$@"`,
    "",
  ].join("\n");
  await writeFile(wrapperPath, script, { encoding: "utf8", mode: 0o755 });
  return wrapperPath;
}

export async function resolveBinaryOnPath(name: string): Promise<string | null> {
  const pathValue = process.env.PATH || "";
  for (const entry of pathValue.split(":")) {
    if (!entry) continue;
    const candidate = join(entry, name);
    try {
      await access(candidate, fsConstants.X_OK);
      return candidate;
    } catch {
      continue;
    }
  }
  return null;
}
