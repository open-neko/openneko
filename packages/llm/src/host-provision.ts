import { mkdir, writeFile, chmod } from "node:fs/promises";
import { homedir, platform } from "node:os";
import { join } from "node:path";

import { and, data_source, db, desc, eq, llm_provider_config } from "@neko/db";
import { isAgentBackendId } from "./agent-backend";
import { maybeDecryptSecret } from "./secrets";
import { ensureOpenShellProvider } from "./work/sandbox-launcher";

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
  return process.env.HOME || homedir();
}

function graphjinConfigPath(): string {
  const xdg = process.env.XDG_CONFIG_HOME?.trim();
  if (xdg) {
    return join(xdg, "graphjin", "client.json");
  }
  if (platform() === "darwin") {
    return join(getHome(), "Library", "Application Support", "graphjin", "client.json");
  }
  return join(getHome(), ".config", "graphjin", "client.json");
}

function deriveServerBase(graphqlUrl: string): string | null {
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
    .orderBy(desc(data_source.is_default), data_source.created_at)
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

const SOURCES_SECRET_PLACEHOLDER = "REPLACE_WITH_PER_ORG_SECRET_B64";

/**
 * Sources-mode (agentic) bootstrap. Two halves, both idempotent:
 *
 * 1. When the deployment mounts the GraphJin config into the worker
 *    (OPENNEKO_GRAPHJIN_CONFIG, the demo compose does), substitute the
 *    per-org JWT secret into the seeded template — the org id only
 *    exists after first boot, so compose can't bake it. GraphJin's
 *    reload_on_config_change picks the write up live.
 *
 * 2. Probe the default data source with a minted service token: if the
 *    server answers a gj_catalog query, it runs agentic sources mode —
 *    flip data_source.auth_mode to 'jwt' so GJ4 actor tokens and the
 *    slim catalog knowledge layering switch on automatically. Legacy
 *    servers fail the probe (no gj_catalog root) and stay 'none'.
 */
async function provisionGraphjinSourcesMode(orgId: string): Promise<void> {
  const cfgPath = process.env.OPENNEKO_GRAPHJIN_CONFIG?.trim();
  if (cfgPath) {
    const { readFile } = await import("node:fs/promises");
    try {
      const raw = await readFile(cfgPath, "utf8");
      if (raw.includes(SOURCES_SECRET_PLACEHOLDER)) {
        const { graphjinSigningSecretB64 } = await import("./graphjin/token");
        await writeFile(
          cfgPath,
          raw.replaceAll(SOURCES_SECRET_PLACEHOLDER, graphjinSigningSecretB64(orgId)),
          "utf8",
        );
        console.log(
          `[host-provision] wrote per-org JWT secret into ${cfgPath} (sources mode)`,
        );
      }
    } catch (e) {
      console.warn(
        `[host-provision] graphjin config secret write failed: ${e instanceof Error ? e.message : e}`,
      );
    }
  }

  const [src] = await db()
    .select({
      id: data_source.id,
      authMode: data_source.auth_mode,
      graphqlUrl: data_source.graphql_url,
    })
    .from(data_source)
    .where(eq(data_source.org_id, orgId))
    .orderBy(desc(data_source.is_default), data_source.created_at)
    .limit(1);
  if (!src?.graphqlUrl || src.authMode === "jwt") return;

  const { mintGraphjinToken } = await import("./graphjin/token");
  const token = mintGraphjinToken({ orgId, userId: null, role: "service" });
  // The secret write above may still be reloading server-side — give
  // reload_on_config_change a generous window (observed ~10s on 3.18.37).
  for (let attempt = 1; attempt <= 6; attempt++) {
    try {
      const res = await fetch(src.graphqlUrl, {
        method: "POST",
        headers: {
          "content-type": "application/json",
          authorization: `Bearer ${token}`,
        },
        body: JSON.stringify({
          query: `query { gj_catalog(id: "help:discovery") { id } }`,
        }),
        signal: AbortSignal.timeout(5_000),
      });
      if (res.ok) {
        const body = (await res.json()) as {
          data?: { gj_catalog?: unknown };
          errors?: unknown[];
        };
        // gj_catalog(id:) returns one object; list queries an array.
        const rows = body.data?.gj_catalog;
        const answered = Array.isArray(rows) ? rows.length > 0 : Boolean(rows);
        if (answered && !body.errors?.length) {
          await db()
            .update(data_source)
            .set({ auth_mode: "jwt", updated_at: new Date() })
            .where(eq(data_source.id, src.id));
          console.log(
            `[host-provision] data source answers gj_catalog with an actor token — auth_mode=jwt (agentic mode on)`,
          );
          return;
        }
      }
    } catch {
      // unreachable / not reloaded yet — retry below.
    }
    if (attempt < 6) await new Promise((r) => setTimeout(r, 3000));
  }
}

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

// API host per neko model-provider when there's no explicit base_url. A handful
// of providers (NOT the 300 model variants) — derived from the org's config,
// never hand-set per deployment.
const PROVIDER_API_HOSTS: Record<string, string> = {
  "google-gemini": "generativelanguage.googleapis.com",
  anthropic: "api.anthropic.com",
  openai: "api.openai.com",
  openrouter: "openrouter.ai",
  "x-grok": "openrouter.ai",
};

/**
 * Derive the agent-sandbox egress from the org's model config: the API host
 * (explicit base_url, else the provider default) + models.dev for hermes model
 * resolution; the connecting binary (per backend + arch — egress matches the
 * resolved path); the env var the backend reads. Binary paths track the agent
 * image's pinned cpython.
 */
function deriveAgentEgress(
  row: StoredRow,
  backend: string,
): { hosts: string[]; binary: string; keyEnv: string } {
  const cfg = (row.config ?? {}) as { url?: string; baseUrl?: string };
  const baseUrl = cfg.baseUrl || cfg.url;
  let host: string | undefined;
  if (baseUrl) {
    try {
      host = new URL(baseUrl).host;
    } catch {
      /* fall through to the provider default */
    }
  }
  host ??= PROVIDER_API_HOSTS[row.provider];
  const isClaude = backend === "claude-agent";
  const arch = process.arch === "arm64" ? "aarch64" : "x86_64";
  return {
    hosts: [...(host ? [host] : []), ...(isClaude ? [] : ["models.dev"])],
    binary: isClaude
      ? "/usr/local/lib/node_modules/@anthropic-ai/claude-code/bin/claude.exe"
      : `/usr/local/uv/python/cpython-3.11.15-linux-${arch}-gnu/bin/python3.11`,
    keyEnv: isClaude ? "ANTHROPIC_API_KEY" : mapHermesProvider(row.provider).keyVar,
  };
}

/**
 * OPENNEKO_AGENT_RUNTIME=openshell: self-configure the agent sandbox from the
 * org's model config — derive the egress (host/binary/key-env; explicit env
 * overrides win) and sync the model key into a gateway-side provider so the
 * proxy injects it on the wire (the key never enters the box). Replaces the
 * manual `openshell provider create` + hand-set egress env.
 */
async function provisionOpenShellRuntime(orgId: string, backend: string): Promise<void> {
  if ((process.env.OPENNEKO_AGENT_RUNTIME ?? "openshell").toLowerCase() !== "openshell") {
    return;
  }
  const row = await loadProviderRow(orgId, "primary");
  if (!row || !row.enabled) return;

  const { hosts, binary, keyEnv } = deriveAgentEgress(row, backend);
  process.env.OPENNEKO_AGENT_MODEL_PROVIDER ||= "openneko-agent";
  if (!process.env.OPENNEKO_AGENT_MODEL_HOST && hosts.length > 0) {
    process.env.OPENNEKO_AGENT_MODEL_HOST = hosts.join(",");
  }
  process.env.OPENNEKO_AGENT_MODEL_BINARY ||= binary;
  process.env.OPENNEKO_AGENT_MODEL_KEY_ENV ||= keyEnv;
  // hermes reads its model + provider from config.yaml under HERMES_HOME. In the
  // sandbox that config must be MIRRORED in — the launcher does so when
  // OPENNEKO_AGENT_HERMES_HOME points at the host home provisionHermes writes.
  // Without it, in-box hermes finds no config and silently falls back to a
  // default model that 404s. (claude takes the model via its backend args, so
  // it needs no hermes home.)
  if (backend !== "claude-agent") {
    process.env.OPENNEKO_AGENT_HERMES_HOME ||= hermesHomeForOrg(orgId);
  }

  const apiKey = decryptSecrets(row.secrets).apiKey;
  if (!apiKey) return;
  await ensureOpenShellProvider({
    providerName: process.env.OPENNEKO_AGENT_MODEL_PROVIDER,
    apiKey,
    gatewayName: process.env.OPENSHELL_GATEWAY || undefined,
    gatewayEndpoint: process.env.OPENSHELL_GATEWAY_ENDPOINT || undefined,
  });
  console.log(
    `[host-provision] OpenShell agent runtime self-configured: provider="${process.env.OPENNEKO_AGENT_MODEL_PROVIDER}" egress="${hosts.join(",")}" keyEnv=${keyEnv}`,
  );
}

export async function provisionHostConfig(orgId: string): Promise<void> {
  try {
    await provisionGraphJin(orgId);
  } catch (e) {
    console.warn(
      `[host-provision] graphjin write failed: ${e instanceof Error ? e.message : e}`,
    );
  }

  try {
    await provisionGraphjinSourcesMode(orgId);
  } catch (e) {
    console.warn(
      `[host-provision] sources-mode provision failed: ${e instanceof Error ? e.message : e}`,
    );
  }

  const agentRow = await loadProviderRow(orgId, "agent");
  const backendCfg = (agentRow?.config ?? {}) as { backend?: unknown };
  const backend =
    typeof backendCfg.backend === "string" && isAgentBackendId(backendCfg.backend)
      ? backendCfg.backend
      : "hermes";

  try {
    await provisionOpenShellRuntime(orgId, backend);
  } catch (e) {
    console.warn(
      `[host-provision] OpenShell runtime provision failed: ${e instanceof Error ? e.message : e}`,
    );
  }

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
