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
  // Two case statements: the first does substring matches on graphql operation
  // keywords (so we still catch `mutation` inside a JSON --args payload); the
  // second pads the args with spaces and matches whole-word subcommands so
  // names like "preserve" or "newest" don't false-positive.
  const script = [
    "#!/usr/bin/env bash",
    "set -euo pipefail",
    "joined=\"$*\"",
    "case \"$joined\" in",
    "  *mutation*|*Mutation*|*subscription*|*Subscription*)",
    "    echo \"Neko Work blocks GraphJin mutations and server-changing commands. Read/query only.\" >&2",
    "    exit 2",
    "    ;;",
    "esac",
    "padded=\" $joined \"",
    "case \"$padded\" in",
    "  *' serve '*|*' config '*|*' migrate '*|*' admin '*|*' new '*|*' secret '*|*' secrets '*)",
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
