/**
 * GraphJin knowledge pack — fetched once and read by agents from disk.
 *
 * Replaces the prior pattern of inlining 30-40 KB of discovery JSON
 * into every agent prompt. Inlining wastes tokens (the same blob
 * appears on every turn / retry) and prevents multi-turn agents from
 * cheaply consulting the schema as needed.
 *
 * Borrowed from the Reckon project's `lib/agent/knowledge-loader.ts`:
 *   1. At server boot, fetch the four discovery JSONs over plain REST.
 *   2. Write them to disk under `<knowledgeRoot>/`.
 *   3. Drop an INDEX.md beside them so the agent knows which file to
 *      consult for what.
 *   4. The agent uses its shell tool (`Bash` for Claude Agent,
 *      `terminal` for Hermes) to `cat` / `Read` the files when needed.
 *
 * Per-org isolation: each org's knowledgeRoot lives under its own
 * workspace, so multi-tenant deployments naturally separate. The
 * worker prefetches for the admin org at boot today; settings saves
 * can call `prefetchKnowledgePack` to refresh after a graphjin
 * config change.
 */

import { mkdir, writeFile } from "node:fs/promises";
import { join } from "node:path";

export const KNOWLEDGE_SECTIONS = [
  { section: "tables?limit=500", file: "tables.json", label: "tables" },
  { section: "namespaces", file: "namespaces.json", label: "namespaces" },
  { section: "insights", file: "insights.json", label: "insights" },
  { section: "syntax", file: "syntax.json", label: "syntax" },
] as const;

export const KNOWLEDGE_INDEX_FILE = "INDEX.md";

const INDEX_BODY = `# GraphJin knowledge pack

This directory holds JSON files prefetched from the running GraphJin
server's HTTP discovery endpoints (\`/api/v1/discovery/{section}\`),
plus this index. The four JSONs are regenerated on every worker boot
and on demand. Do NOT run \`graphjin cli list_tables\` /
\`describe_table\` / \`get_query_syntax\` / etc. — that information is
already on disk here.

## Files

- **\`tables.json\`** — every table in the database with name, schema,
  database, type, column count. Read this when you need to know which
  tables exist before constructing a query.

- **\`namespaces.json\`** — the namespaces / databases configured in
  GraphJin (when there are multiple). For most analytics tasks the
  default namespace is fine; consult this only when a query has to
  target a non-default database.

- **\`insights.json\`** — hub tables, hot relationships, query
  templates, data-quality flags. Read this **first** when planning a
  multi-table query; it surfaces the central tables, the most common
  joins, and where GraphJin has detected likely-duplicate /
  denormalised data you should treat carefully.

- **\`syntax.json\`** — the GraphJin DSL reference (operators,
  aggregations, pagination, ordering, expression aggregates,
  directives, common mistakes). Read this when authoring a non-trivial
  query — especially aggregations (\`count_*\`, \`sum(expr: ...)\`,
  \`avg(...)\`) and \`where:\` predicates.

## Running queries

After consulting the files above, run queries via your shell tool:

\`\`\`
graphjin cli execute_graphql --args '{"query":"<read-only graphql>"}'
\`\`\`

The CLI takes its arguments as a single JSON object via \`--args\`. Use
\`--args-file <path>\` (or \`--args-file -\` for stdin) when the GraphQL
body is long enough that quoting it inline gets unwieldy.

If a query returns errors, run:

\`\`\`
graphjin cli fix_query_error --args '{"query":"<failing>","error":"<msg>"}'
\`\`\`

to get a corrected query, then run \`execute_graphql\` again.

**Mutations and subscriptions are not allowed.** Any query containing
the words \`mutation\` or \`subscription\` is denied at the tool gate.
`;

export type PrefetchKnowledgeResult = {
  ok: boolean;
  files: { file: string; bytes: number }[];
  error?: string;
};

export type KnowledgePackPaths = {
  /** Absolute filesystem path of the knowledge directory. */
  knowledgeRoot: string;
  /** Absolute paths the agent should reference in prompts / read with its shell tool. */
  files: {
    tables: string;
    namespaces: string;
    insights: string;
    syntax: string;
    index: string;
  };
};

/**
 * Compute the absolute paths the agent should reference, without
 * touching the filesystem. Useful for buildPrompt — the prompt only
 * needs the paths, not the file contents.
 */
export function knowledgePackPaths(knowledgeRoot: string): KnowledgePackPaths {
  return {
    knowledgeRoot,
    files: {
      tables: join(knowledgeRoot, "tables.json"),
      namespaces: join(knowledgeRoot, "namespaces.json"),
      insights: join(knowledgeRoot, "insights.json"),
      syntax: join(knowledgeRoot, "syntax.json"),
      index: join(knowledgeRoot, KNOWLEDGE_INDEX_FILE),
    },
  };
}

const FETCH_TIMEOUT_MS = 10_000;
const MAX_RETRIES = 3;

async function fetchSection(
  discoveryUrl: string,
  section: string,
  label: string,
): Promise<string> {
  const url = `${discoveryUrl.replace(/\/+$/, "")}/${section}`;
  let lastErr: unknown;
  for (let attempt = 1; attempt <= MAX_RETRIES; attempt++) {
    const ctrl = new AbortController();
    const timeout = setTimeout(() => ctrl.abort(), FETCH_TIMEOUT_MS);
    try {
      const res = await fetch(url, {
        headers: { Accept: "application/json" },
        signal: ctrl.signal,
      });
      if (!res.ok) {
        throw new Error(`${res.status} ${res.statusText}`);
      }
      return JSON.stringify(await res.json(), null, 2);
    } catch (e) {
      lastErr = e;
      if (attempt < MAX_RETRIES) {
        // Exponential backoff (1s, 2s, 4s) — same shape Reckon uses.
        await new Promise((r) => setTimeout(r, 1000 * 2 ** (attempt - 1)));
      }
    } finally {
      clearTimeout(timeout);
    }
  }
  const msg = lastErr instanceof Error ? lastErr.message : String(lastErr);
  throw new Error(
    `discovery ${label} fetch failed after ${MAX_RETRIES} attempts: ${msg}`,
  );
}

/**
 * Fetch the four discovery JSONs from `discoveryUrl` (e.g.
 * `http://graphjin:8080/api/v1/discovery`) and write them — plus
 * INDEX.md — under `destDir`.
 *
 * Best-effort: returns `ok:false` with `error` set if any section
 * fails (caller logs and continues; agent runs will fall back to
 * tool-based discovery).
 */
export async function prefetchKnowledgePack(args: {
  discoveryUrl: string;
  destDir: string;
}): Promise<PrefetchKnowledgeResult> {
  const { discoveryUrl, destDir } = args;
  await mkdir(destDir, { recursive: true });
  try {
    const results = await Promise.all(
      KNOWLEDGE_SECTIONS.map((s) => fetchSection(discoveryUrl, s.section, s.label)),
    );
    const written: { file: string; bytes: number }[] = [];
    for (let i = 0; i < KNOWLEDGE_SECTIONS.length; i++) {
      const file = KNOWLEDGE_SECTIONS[i].file;
      const json = results[i];
      await writeFile(join(destDir, file), json, "utf8");
      written.push({ file, bytes: json.length });
    }
    await writeFile(join(destDir, KNOWLEDGE_INDEX_FILE), INDEX_BODY, "utf8");
    return { ok: true, files: written };
  } catch (e) {
    return {
      ok: false,
      files: [],
      error: e instanceof Error ? e.message : String(e),
    };
  }
}

/**
 * Derive the discovery base URL from a stored `data_source.mcp_url`.
 * Both metric-agent and the worker boot used to compute this inline;
 * shared here so the heuristic stays in one place.
 *
 * Example:
 *   mcp_url = "http://localhost:8080/api/v1/mcp"
 *   ->        "http://localhost:8080/api/v1/discovery"
 */
export function discoveryUrlFromMcpUrl(mcpUrl: string): string {
  return mcpUrl.replace(/\/mcp\/?$/, "/discovery");
}
