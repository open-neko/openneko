import { writeFile } from "node:fs/promises";
import { constants as fsConstants } from "node:fs";
import { access } from "node:fs/promises";
import { join } from "node:path";

/**
 * GraphJin write-path subcommands. The agent's read-only contract
 * (set in the prompt + the GraphJin knowledge pack) drives every query
 * through `graphjin cli execute_graphql --args '{"query":"..."}'`.
 * These subcommands mutate server state and must be blocked even if
 * the model gets creative.
 *
 * Mirrors Reckon's `inspectBashForGraphjinMutations` denylist —
 * the agent runs the same `graphjin cli` shape there too.
 */
const WRITE_SUBCOMMANDS = [
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

export function isGraphjinCommandSafe(args: string[]): boolean {
  const joined = args.join(" ");
  // Direct write subcommand: `graphjin (cli )?save_workflow …` etc.
  for (const sub of WRITE_SUBCOMMANDS) {
    if (new RegExp(`\\b${sub}\\b`).test(joined)) return false;
  }
  // Executor-style commands carrying a mutation or subscription op.
  const isExecutor = EXECUTOR_SUBCOMMANDS.some((sub) =>
    new RegExp(`\\b${sub}\\b`).test(joined),
  );
  if (isExecutor && /\b(mutation|subscription)\b/i.test(joined)) return false;
  return true;
}

export async function ensureGraphjinGuard(
  binRoot: string,
  graphjinBinary: string,
): Promise<string> {
  const wrapperPath = join(binRoot, "graphjin");
  const writeAlt = WRITE_SUBCOMMANDS.map((s) => `*' ${s} '*|*' ${s}'`).join("|");
  const execAlt = EXECUTOR_SUBCOMMANDS.map((s) => `*' ${s} '*|*' ${s}'`).join("|");
  const script = [
    "#!/usr/bin/env bash",
    "set -euo pipefail",
    "padded=\" $* \"",
    "case \"$padded\" in",
    `  ${writeAlt})`,
    "    echo \"Neko Work blocks GraphJin write subcommands. Read/query only.\" >&2",
    "    exit 2",
    "    ;;",
    "esac",
    `case "$padded" in`,
    `  ${execAlt})`,
    "    if [[ \"$padded\" =~ (^|[^[:alnum:]_])(mutation|subscription)([^[:alnum:]_]|$) ]]; then",
    "      echo \"Neko Work blocks GraphJin mutations and subscriptions. Read/query only.\" >&2",
    "      exit 2",
    "    fi",
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
