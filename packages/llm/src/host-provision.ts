import { mkdir, writeFile, chmod } from "node:fs/promises";
import { homedir, platform } from "node:os";
import { join } from "node:path";

import { and, data_source, db, eq, llm_provider_config } from "@neko/db";
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

/**
 * When the agent runs in an OpenShell sandbox, sync the org's primary model key
 * into a gateway-side provider so the egress proxy can inject it on the wire —
 * the key never enters the sandbox. Replaces the manual `openshell provider
 * create` step. Egress + the key-env alias stay env-wired in the launcher.
 */
async function provisionOpenShellProvider(orgId: string): Promise<void> {
  if ((process.env.OPENNEKO_AGENT_RUNTIME ?? "").toLowerCase() !== "openshell") {
    return;
  }
  const providerName = process.env.OPENNEKO_AGENT_MODEL_PROVIDER;
  if (!providerName) return;
  const row = await loadProviderRow(orgId, "primary");
  if (!row || !row.enabled) return;
  const apiKey = decryptSecrets(row.secrets).apiKey;
  if (!apiKey) return;
  await ensureOpenShellProvider({
    providerName,
    apiKey,
    gatewayName: process.env.OPENSHELL_GATEWAY || undefined,
    gatewayEndpoint: process.env.OPENSHELL_GATEWAY_ENDPOINT || undefined,
  });
  console.log(`[host-provision] synced OpenShell model provider "${providerName}"`);
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
    await provisionOpenShellProvider(orgId);
  } catch (e) {
    console.warn(
      `[host-provision] OpenShell provider sync failed: ${e instanceof Error ? e.message : e}`,
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
