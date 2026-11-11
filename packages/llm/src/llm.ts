import { and, db, eq, llm_provider_config } from "@neko/db";
import {
  type EditableProviderConfig,
  type PrimaryProviderId,
  type StoredProviderConfigRow,
  getDefaultPrimaryModel,
  isPrimaryProvider,
  readPrimaryProviderConfigFromEnv,
} from "./config";
import { maybeDecryptSecret } from "./secrets";
import { GoogleAuth } from "google-auth-library";

export type ProviderSource = "org" | "env" | "default" | "draft";

export type ResolvedPrimaryProviderConfig = {
  source: ProviderSource;
  provider: PrimaryProviderId;
  model: string;
  enabled: boolean;
  label?: string | null;
  config: Record<string, unknown>;
  secrets: Record<string, string>;
};

const googleAuth = new GoogleAuth({
  scopes: ["https://www.googleapis.com/auth/cloud-platform"],
});

function defaultPrimaryConfig(): ResolvedPrimaryProviderConfig {
  return {
    source: "default",
    provider: "google-gemini",
    model: getDefaultPrimaryModel("google-gemini"),
    enabled: true,
    label: null,
    config: {},
    secrets: {},
  };
}

async function loadStoredPrimaryConfig(
  orgId: string,
): Promise<StoredProviderConfigRow | null> {
  try {
    const rows = await db()
      .select({
        id: llm_provider_config.id,
        org_id: llm_provider_config.org_id,
        scope: llm_provider_config.scope,
        provider: llm_provider_config.provider,
        model: llm_provider_config.model,
        label: llm_provider_config.label,
        enabled: llm_provider_config.enabled,
        config: llm_provider_config.config,
        secrets: llm_provider_config.secrets,
      })
      .from(llm_provider_config)
      .where(
        and(
          eq(llm_provider_config.org_id, orgId),
          eq(llm_provider_config.scope, "primary"),
        ),
      )
      .limit(1);
    return (rows[0] as StoredProviderConfigRow | undefined) ?? null;
  } catch {
    return null;
  }
}

function decryptSecrets(row: StoredProviderConfigRow | null): Record<string, string> {
  const secrets = row?.secrets ?? {};
  const decoded: Record<string, string> = {};

  for (const [key, value] of Object.entries(secrets)) {
    const decrypted = maybeDecryptSecret(value);
    if (decrypted) decoded[key] = decrypted;
  }

  return decoded;
}

function validateResolvedConfig(config: ResolvedPrimaryProviderConfig): void {
  switch (config.provider) {
    case "ollama":
      if (!String(config.config.url ?? "").trim()) {
        throw new Error("OLLAMA provider requires a Base URL");
      }
      return;
    case "openai-compatible":
      if (!String(config.config.url ?? "").trim()) {
        throw new Error("An OpenAI-compatible provider needs a Base URL");
      }
      if (!config.model.trim()) {
        throw new Error("An OpenAI-compatible provider needs a model name");
      }
      return;
    case "azure-openai":
      if (!config.secrets.apiKey) {
        throw new Error("Azure OpenAI requires an API key");
      }
      if (!String(config.config.resourceName ?? "").trim()) {
        throw new Error("Azure OpenAI requires a resource name");
      }
      if (!String(config.config.deploymentName ?? "").trim()) {
        throw new Error("Azure OpenAI requires a deployment name");
      }
      return;
    case "vertex":
      if (!String(config.config.projectId ?? "").trim()) {
        throw new Error("Vertex requires a GCP project ID");
      }
      return;
    default:
      if (!config.secrets.apiKey) {
        throw new Error(`${config.provider} requires an API key`);
      }
  }
}

export async function resolvePrimaryProviderConfig(
  orgId?: string,
  draft?: EditableProviderConfig,
): Promise<ResolvedPrimaryProviderConfig> {
  if (draft) {
    if (draft.scope !== "primary") {
      throw new Error(`Expected primary config draft, got ${draft.scope}`);
    }

    let mergedSecrets: Record<string, string> = { ...draft.secrets };
    let mergedConfig: Record<string, unknown> = { ...draft.config };
    if (orgId) {
      const stored = await loadStoredPrimaryConfig(orgId);
      if (stored && isPrimaryProvider(stored.provider) && stored.provider === draft.provider) {
        const storedSecrets = decryptSecrets(stored);
        mergedSecrets = { ...storedSecrets, ...draft.secrets };
        const storedConfig = (stored.config as Record<string, unknown>) ?? {};
        mergedConfig = { ...storedConfig, ...draft.config };
      }
    }

    const resolved: ResolvedPrimaryProviderConfig = {
      source: "draft",
      provider: draft.provider,
      model: draft.model || getDefaultPrimaryModel(draft.provider),
      enabled: draft.enabled,
      label: draft.label ?? null,
      config: mergedConfig,
      secrets: mergedSecrets,
    };
    validateResolvedConfig(resolved);
    return resolved;
  }

  if (orgId) {
    const stored = await loadStoredPrimaryConfig(orgId);
    if (stored && isPrimaryProvider(stored.provider)) {
      const resolved: ResolvedPrimaryProviderConfig = {
        source: "org",
        provider: stored.provider,
        model: stored.model ?? getDefaultPrimaryModel(stored.provider),
        enabled: stored.enabled,
        label: stored.label ?? null,
        config: (stored.config as Record<string, unknown>) ?? {},
        secrets: decryptSecrets(stored),
      };
      validateResolvedConfig(resolved);
      return resolved;
    }
  }

  const env = readPrimaryProviderConfigFromEnv();
  if (env && env.scope === "primary") {
    const resolved: ResolvedPrimaryProviderConfig = {
      source: "env",
      provider: env.provider,
      model: env.model || getDefaultPrimaryModel(env.provider),
      enabled: env.enabled,
      label: env.label ?? null,
      config: env.config,
      secrets: env.secrets,
    };
    validateResolvedConfig(resolved);
    return resolved;
  }

  const fallback = defaultPrimaryConfig();
  return fallback;
}

export async function getGoogleToken(): Promise<string> {
  const client = await googleAuth.getClient();
  const res = await client.getAccessToken();
  if (!res.token) {
    throw new Error(
      "Failed to obtain Google access token. Use gcloud ADC or GOOGLE_APPLICATION_CREDENTIALS.",
    );
  }
  return res.token;
}

export async function verifyAiCredentials(): Promise<void> {
  let rows: StoredProviderConfigRow[];
  try {
    rows = (await db()
      .select({
        id: llm_provider_config.id,
        org_id: llm_provider_config.org_id,
        scope: llm_provider_config.scope,
        provider: llm_provider_config.provider,
        model: llm_provider_config.model,
        label: llm_provider_config.label,
        enabled: llm_provider_config.enabled,
        config: llm_provider_config.config,
        secrets: llm_provider_config.secrets,
      })
      .from(llm_provider_config)
      .where(
        and(
          eq(llm_provider_config.scope, "primary"),
          eq(llm_provider_config.enabled, true),
        ),
      )) as StoredProviderConfigRow[];
  } catch {
    rows = [];
  }
  if (rows.length === 0) {
    console.warn(
      "[worker] no primary provider configured for any org; configure one in Settings.",
    );
    return;
  }

  for (const row of rows) {
    if (!isPrimaryProvider(row.provider)) continue;
    try {
      const resolved = await resolvePrimaryProviderConfig(row.org_id);
      if (resolved.provider === "vertex") {
        await getGoogleToken();
      }
      console.log(
        `[worker] org=${row.org_id} provider=${resolved.provider} model=${resolved.model}`,
      );
    } catch (e) {
      console.warn(
        `[worker] org=${row.org_id} provider check failed: ${
          e instanceof Error ? e.message : String(e)
        }`,
      );
    }
  }
}
