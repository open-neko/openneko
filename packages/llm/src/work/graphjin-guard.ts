import { writeFile } from "node:fs/promises";
import { constants as fsConstants } from "node:fs";
import { access } from "node:fs/promises";
import { join } from "node:path";

/**
 * GraphJin write-path subcommands. Even when the agent goes through
 * `graphjin cli`, these mutate server state and must be blocked.
 *
 * Mirrors Reckon's `inspectBashForGraphjinMutations` denylist.
 */
const WRITE_SUBCOMMANDS = [
  "setup",
  "config",
  "write_query",
  "write_mutation",
  "save_workflow",
  "update_current_config",
  "apply_schema_changes",
  "reload_schema",
  "apply_database_setup",
  "preview_schema_changes",
] as const;

const EXECUTOR_SUBCOMMANDS = [
  "execute_graphql",
  "execute_saved_query",
  "execute_workflow",
] as const;

/**
 * Allowlist gate: the agent's contract is `graphjin cli <subcommand>` only.
 * Anything else (`serve`, `migrate`, `admin`, bare invocation, etc.) is
 * denied. Within `cli`, write subcommands and mutation/subscription ops
 * are denied; everything else passes.
 */
export function isGraphjinCommandSafe(args: string[]): boolean {
  if (args.length === 0) return false;
  if (args[0] !== "cli") return false;

  const sub = args[1];
  if (!sub) return false;
  if ((WRITE_SUBCOMMANDS as readonly string[]).includes(sub)) return false;

  if ((EXECUTOR_SUBCOMMANDS as readonly string[]).includes(sub)) {
    const joined = args.slice(2).join(" ");
    if (/\b(mutation|subscription)\b/i.test(joined)) return false;
  }
  return true;
}

export async function ensureGraphjinGuard(
  binRoot: string,
  graphjinBinary: string,
): Promise<string> {
  const wrapperPath = join(binRoot, "graphjin");
  const writeAlt = WRITE_SUBCOMMANDS.join("|");
  const execAlt = EXECUTOR_SUBCOMMANDS.join("|");
  const pinnedXdgConfigHome = process.env.XDG_CONFIG_HOME?.trim() || "";
  const script = [
    "#!/usr/bin/env bash",
    "set -euo pipefail",
    "",
    pinnedXdgConfigHome
      ? `export XDG_CONFIG_HOME=${shellQuote(pinnedXdgConfigHome)}`
      : "",
    "",
    "if [[ \"${1:-}\" != \"cli\" ]]; then",
    "  echo \"Neko Work allows only 'graphjin cli <subcommand>'. Direct '${1:-(none)}' invocations are not permitted.\" >&2",
    "  exit 2",
    "fi",
    "",
    "sub=\"${2:-}\"",
    "case \"$sub\" in",
    `  ${writeAlt})`,
    "    echo \"Neko Work blocks GraphJin write subcommands. Read/query only.\" >&2",
    "    exit 2",
    "    ;;",
    "esac",
    "",
    "case \"$sub\" in",
    `  ${execAlt})`,
    "    rest=\"${*:3}\"",
    "    if [[ \"$rest\" =~ (^|[^[:alnum:]_])(mutation|subscription)([^[:alnum:]_]|$) ]]; then",
    "      echo \"Neko Work blocks GraphJin mutations and subscriptions. Read/query only.\" >&2",
    "      exit 2",
    "    fi",
    "    ;;",
    "esac",
    "",
    `exec "${graphjinBinary}" "$@"`,
    "",
  ].join("\n");
  await writeFile(wrapperPath, script, { encoding: "utf8", mode: 0o755 });
  return wrapperPath;
}

function shellQuote(value: string): string {
  return `'${value.replace(/'/g, `'\\''`)}'`;
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
