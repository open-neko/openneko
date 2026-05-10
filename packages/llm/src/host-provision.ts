/**
 * Host-side config provisioning for the metric agent.
 *
 * Replaces the old `packages/llm/scripts/hermes-bootstrap.sh` operator script
 * — instead of asking a human to set env vars and run a shell command, the
 * worker reads everything from the DB on boot and writes the host config
 * files itself. The web app calls the same function from the settings save
 * handlers so changes apply without a worker restart.
 *
 * Files written:
 *   - GraphJin client config: tells `graphjin cli` which server to talk to.
 *       macOS:   ~/Library/Application Support/graphjin/client.json
 *       linux:   ~/.config/graphjin/client.json
 *     Format: { server, token: "", expires_at: "0001-01-01T00:00:00Z" }
 *
 *   - Hermes config (only when agent backend = hermes):
 *       <hermesHome>/config.yaml — model.default, model.provider, agent.max_turns
 *       <hermesHome>/.env       — provider key (e.g. ANTHROPIC_API_KEY=...)
 *
 *     Where `<hermesHome>` is resolved by `hermesHomeForOrg(orgId)`:
 *       1. process.env.HERMES_HOME (test fixture / operator escape hatch), or
 *       2. ~/.config/openneko/hermes/{orgId}/  (per-org isolation — default)
 *
 *     Per-org isolation matters because Hermes maintains a credential pool
 *     in `<hermesHome>/auth.json` that's shared across every `hermes`
 *     invocation reading the same home. If we used the user's global
 *     ~/.hermes/, anything they did in their own terminal (`hermes login`,
 *     stale OAuth tokens from `claude_code`, etc.) could poison our
 *     long-running agents. Per-org dirs sidestep that drift entirely.
 *
 *   The Claude Agent backend has no host config — its API key is passed
 *   per-call via `env` in the SDK options, sourced from the same DB row.
 *
 * Single-host assumption: the worker process must run on the same machine
 * whose home directory we provision. Multi-host deployments will need to
 * shift to in-process credentials for Hermes too (pass via flags or a
 * temp config dir), or sync the home dir across hosts.
 */

import { mkdir, writeFile, chmod } from "node:fs/promises";
import { homedir, platform } from "node:os";
import { join } from "node:path";

import { and, data_source, db, eq, llm_provider_config } from "@neko/db";
import { isAgentBackendId } from "./agent-backend";
import { maybeDecryptSecret } from "./secrets";

const HERMES_DEFAULT_MAX_TURNS = 25;

type StoredRow = {
  provider: string;
  model: string | null;
  enabled: boolean;
  config: Record<string, unknown> | null;
  secrets: Record<string, unknown> | null;
};

async function loadProviderRow(
  orgId: string,
  scope: string,
): Promise<StoredRow | null> {
  try {
    const rows = await db()
      .select({
        provider: llm_provider_config.provider,
        model: llm_provider_config.model,
        enabled: llm_provider_config.enabled,
        config: llm_provider_config.config,
        secrets: llm_provider_config.secrets,
      })
      .from(llm_provider_config)
      .where(
        and(
          eq(llm_provider_config.org_id, orgId),
          eq(llm_provider_config.scope, scope),
        ),
      )
      .limit(1);
    return (rows[0] as StoredRow | undefined) ?? null;
  } catch {
    return null;
  }
}

function getHome(): string {
  // Prefer process.env.HOME so tests can redirect writes to a temp dir.
  // os.homedir() can ignore env overrides on some platforms (and is
  // sometimes cached at first call), which makes it unsafe for tests.
  return process.env.HOME || homedir();
}

function graphjinConfigPath(): string {
  if (platform() === "darwin") {
    return join(getHome(), "Library", "Application Support", "graphjin", "client.json");
  }
  return join(getHome(), ".config", "graphjin", "client.json");
}

function deriveServerBase(graphqlUrl: string): string | null {
  // graphjin cli wants the server's base URL, not the GraphQL endpoint.
  // Strip the standard /api/v1/graphql suffix; if the URL doesn't match,
  // fall back to dropping the path entirely.
  try {
    const u = new URL(graphqlUrl);
    if (u.pathname.endsWith("/api/v1/graphql")) {
      return `${u.origin}${u.pathname.slice(0, -"/api/v1/graphql".length)}`;
    }
    return u.origin;
  } catch {
    return null;
  }
}

async function provisionGraphJin(orgId: string): Promise<void> {
  const rows = await db()
    .select({ graphql_url: data_source.graphql_url })
    .from(data_source)
    .where(eq(data_source.org_id, orgId))
    .limit(1);
  const url = rows[0]?.graphql_url;
  if (!url) return;

  const server = deriveServerBase(url);
  if (!server) return;

  const path = graphjinConfigPath();
  await mkdir(join(path, ".."), { recursive: true });
  await writeFile(
    path,
    JSON.stringify(
      { server, token: "", expires_at: "0001-01-01T00:00:00Z" },
      null,
      2,
    ),
    "utf8",
  );
}

// Map a Neko provider id → the (provider, key-env-var) Hermes expects.
// `custom` is Hermes's "OpenAI-compat with explicit base_url" mode.
function mapHermesProvider(neko: string): {
  provider: string;
  keyVar: string;
  needsBaseUrl: boolean;
} {
  switch (neko) {
    case "anthropic":
      return { provider: "anthropic", keyVar: "ANTHROPIC_API_KEY", needsBaseUrl: false };
    case "openai":
      return { provider: "openai", keyVar: "OPENAI_API_KEY", needsBaseUrl: false };
    case "openrouter":
      return { provider: "openrouter", keyVar: "OPENROUTER_API_KEY", needsBaseUrl: false };
    case "google-gemini":
      return { provider: "gemini", keyVar: "GEMINI_API_KEY", needsBaseUrl: false };
    case "vertex":
      return { provider: "custom", keyVar: "OPENAI_API_KEY", needsBaseUrl: true };
    case "x-grok":
      return { provider: "openrouter", keyVar: "OPENROUTER_API_KEY", needsBaseUrl: false };
    case "ollama":
    case "azure-openai":
      return {
        provider: "custom",
        keyVar: `${neko.toUpperCase().replace(/-/g, "_")}_API_KEY`,
        needsBaseUrl: true,
      };
    default:
      return {
        provider: neko,
        keyVar: `${neko.toUpperCase().replace(/-/g, "_")}_API_KEY`,
        needsBaseUrl: false,
      };
  }
}

function decryptSecrets(secrets: Record<string, unknown> | null): Record<string, string> {
  if (!secrets) return {};
  const out: Record<string, string> = {};
  for (const [k, v] of Object.entries(secrets)) {
    const decrypted = maybeDecryptSecret(v);
    if (decrypted) out[k] = decrypted;
  }
  return out;
}

/**
 * Resolve the HERMES_HOME path used for `orgId`.
 *
 *   1. process.env.HERMES_HOME — explicit override (tests, operators).
 *   2. ~/.config/openneko/hermes/{orgId}/ — per-org default.
 *
 * The XDG_CONFIG_HOME convention is honored via the same path that
 * secrets.ts and local-config.ts use, so a custom XDG dir lands all of
 * Neko's per-host config under one tree.
 *
 * This is the ONE place that decides where Hermes' home lives. Both
 * `provisionHermes` (writes config.yaml + .env) and the Hermes backend
 * (sets HERMES_HOME on spawn) call it, so the writer and the reader
 * agree by construction.
 */
export function hermesHomeForOrg(orgId: string): string {
  const override = process.env.HERMES_HOME?.trim();
  if (override) return override;
  const xdg = process.env.XDG_CONFIG_HOME?.trim();
  const base = xdg && xdg.length > 0
    ? xdg
    : join(getHome(), ".config");
  return join(base, "openneko", "hermes", orgId);
}

async function provisionHermes(orgId: string): Promise<void> {
  const row = await loadProviderRow(orgId, "primary");
  if (!row || !row.enabled) return;

  const { provider, keyVar, needsBaseUrl } = mapHermesProvider(row.provider);
  const cfg = (row.config ?? {}) as { url?: string; baseUrl?: string };
  const baseUrl = cfg.baseUrl || cfg.url;
  const secrets = decryptSecrets(row.secrets);
  const apiKey = secrets.apiKey;
  const model = row.model ?? "";

  const hermesHome = hermesHomeForOrg(orgId);
  await mkdir(hermesHome, { recursive: true });

  const yamlLines = [
    "model:",
    `  default: "${escapeYamlString(model)}"`,
    `  provider: "${provider}"`,
  ];
  if (baseUrl) yamlLines.push(`  base_url: "${escapeYamlString(baseUrl)}"`);
  if (provider === "custom" && needsBaseUrl && !baseUrl) {
    // Don't fail boot — just skip; the user will see Hermes's own startup
    // error when a job runs. Logging here is enough.
    console.warn(
      `[host-provision] hermes: provider=custom but no base_url for ${row.provider}; user must add it in /settings/agent`,
    );
  }
  yamlLines.push("");
  yamlLines.push("agent:");
  yamlLines.push(`  max_turns: ${HERMES_DEFAULT_MAX_TURNS}`);
  yamlLines.push("");

  await writeFile(join(hermesHome, "config.yaml"), yamlLines.join("\n"), "utf8");

  const envContent = apiKey ? `${keyVar}=${apiKey}\n` : "";
  await writeFile(join(hermesHome, ".env"), envContent, { encoding: "utf8", mode: 0o600 });
  await chmod(join(hermesHome, ".env"), 0o600);
}

function escapeYamlString(s: string): string {
  return s.replace(/\\/g, "\\\\").replace(/"/g, '\\"');
}

/**
 * Write the host config files for `orgId` based on the current DB state.
 *
 * Always provisions GraphJin (both backends shell out to `graphjin cli`).
 * Provisions Hermes only when the resolved agent backend is hermes — the
 * Claude Agent takes its key per-call.
 *
 * Best-effort: failures (missing rows, file-system errors) log a warning
 * and return so the caller (worker boot or settings PUT) can keep going.
 */
export async function provisionHostConfig(orgId: string): Promise<void> {
  try {
    await provisionGraphJin(orgId);
  } catch (e) {
    console.warn(
      `[host-provision] graphjin write failed: ${e instanceof Error ? e.message : e}`,
    );
  }

  const agentRow = await loadProviderRow(orgId, "agent");
  const backendCfg = (agentRow?.config ?? {}) as { backend?: unknown };
  const backend =
    typeof backendCfg.backend === "string" && isAgentBackendId(backendCfg.backend)
      ? backendCfg.backend
      : "hermes";

  if (backend === "hermes") {
    try {
      await provisionHermes(orgId);
    } catch (e) {
      console.warn(
        `[host-provision] hermes write failed: ${e instanceof Error ? e.message : e}`,
      );
    }
  }
}
