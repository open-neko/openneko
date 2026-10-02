/** Trusted operator configuration for Harness-only OpenShell model routes. */
export type HarnessRouting = {
  manifest: string;
  providers: readonly string[];
  modelHosts: ReadonlyArray<{ host: string; port?: number }>;
  keyAliases: ReadonlyArray<{ from: string; to: string }>;
};

function boundedUtf8(value: string, limit: number): string {
  const bytes = Buffer.from(value, "utf8");
  if (bytes.length <= limit) return value;
  let end = limit;
  while (end > 0 && (bytes[end]! & 0xc0) === 0x80) end--;
  return bytes.subarray(0, end).toString("utf8");
}

/** Keep the accepted skill query within Go's byte limit on every launch path. */
export function boundedSkillQuery(value: string): string { return boundedUtf8(value, 8192); }

/** Build the same host-owned shadow input for launch and checkpoint recovery. */
export function harnessTriageSpec(input: {
  routingManifest?: string;
  enabled: boolean;
  maxCostMicros?: number;
  prompt: string;
  userMessage?: string;
  mode?: string;
  lookupRead: boolean;
  artifactRequested?: boolean;
}): Record<string, string | number | boolean> {
  if (!input.enabled || !input.maxCostMicros || !input.routingManifest || !JSON.parse(input.routingManifest).triage) return {};
  const summarySource = input.userMessage?.trim() || input.prompt.trim();
  const summary = boundedUtf8(summarySource, 2048);
  if (!summary) return {};
  const families = ["file"];
  if (input.lookupRead) families.push("graphjin");
  if (input.mode === "workflow") families.push("workflow");
  if (input.mode === "work") families.push("mcp");
  return {triage_summary: summary, triage_artifact_requested: input.artifactRequested === true,
    triage_tool_families: families.join(","), triage_input_bytes: Math.min(Buffer.byteLength(summarySource), 131072)};
}

const routeKey = /^[a-z][a-z0-9_-]{0,63}$/;
const envName = /^[A-Z][A-Z0-9_]{1,127}$/;
const harnessKeyEnv = /^HARNESS_[A-Z0-9_]+_KEY$/;
const providerName = /^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$/;
type HarnessPrice = { input_micros_per_million: number; output_micros_per_million: number };
type HarnessBudgetLimits = { max_model_calls: number; max_model_tokens: number; max_cost_micros: number };

function parseBudgetLimits(value: unknown): HarnessBudgetLimits {
  if (!value || typeof value !== "object" || Array.isArray(value)) throw new Error("Invalid Harness budget limits");
  const limits = value as Record<string, unknown>;
  if (Object.keys(limits).some(key => !["max_model_calls", "max_model_tokens", "max_cost_micros"].includes(key)) ||
      !Number.isSafeInteger(limits.max_model_calls) || (limits.max_model_calls as number) < 1 || (limits.max_model_calls as number) > 64 ||
      !Number.isSafeInteger(limits.max_model_tokens) || (limits.max_model_tokens as number) < 1 || (limits.max_model_tokens as number) > 10_000_000 ||
      !Number.isSafeInteger(limits.max_cost_micros) || (limits.max_cost_micros as number) < 1 || (limits.max_cost_micros as number) > 1_000_000_000_000) {
    throw new Error("Invalid Harness budget limits");
  }
  return limits as HarnessBudgetLimits;
}

function parseBudgetPolicy(value: unknown): {version: string; short: HarnessBudgetLimits; multi_step: HarnessBudgetLimits; artifact: HarnessBudgetLimits} {
  if (!value || typeof value !== "object" || Array.isArray(value)) throw new Error("Invalid Harness budget policy");
  const policy = value as Record<string, unknown>;
  if (Object.keys(policy).some(key => !["version", "short", "multi_step", "artifact"].includes(key)) ||
      typeof policy.version !== "string" || !/^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/.test(policy.version)) {
    throw new Error("Invalid Harness budget policy");
  }
  const short = parseBudgetLimits(policy.short);
  const multi_step = parseBudgetLimits(policy.multi_step);
  const artifact = parseBudgetLimits(policy.artifact);
  for (const key of ["max_model_calls", "max_model_tokens", "max_cost_micros"] as const) {
    if (short[key] > multi_step[key] || multi_step[key] > artifact[key]) throw new Error("Invalid Harness budget policy order");
  }
  return {version: policy.version, short, multi_step, artifact};
}

function parsePrice(value: unknown): HarnessPrice {
  if (!value || typeof value !== "object" || Array.isArray(value)) throw new Error("Invalid Harness price");
  const price = value as Record<string, unknown>;
  if (Object.keys(price).some(key => !["input_micros_per_million", "output_micros_per_million"].includes(key)) ||
      ![price.input_micros_per_million, price.output_micros_per_million].every(rate => Number.isSafeInteger(rate) && (rate as number) > 0 && (rate as number) <= 1_000_000_000)) {
    throw new Error("Invalid Harness price");
  }
  return {input_micros_per_million: price.input_micros_per_million as number,
    output_micros_per_million: price.output_micros_per_million as number};
}

/**
 * The operator pre-provisions each named OpenShell provider. This parser
 * produces the exact allowlisted model manifest sent to Go plus the providers,
 * destination hosts and credential aliases the sandbox must receive.
 */
export function parseHarnessRouting(raw: string): HarnessRouting {
  if (!raw || raw.length > 65_536) throw new Error("Invalid Harness routing configuration");
  let config: unknown;
  try { config = JSON.parse(raw); }
  catch { throw new Error("Invalid Harness routing configuration"); }
  if (!config || typeof config !== "object" || Array.isArray(config)) {
    throw new Error("Invalid Harness routing configuration");
  }
  const value = config as Record<string, unknown>;
  if (Object.keys(value).some(key => !["context", "executor", "executor_escalation", "executor_after_errors", "responder", "skill", "triage", "budget_policy", "fallbacks", "routes", "pricing_version", "graphjin_price"].includes(key)) ||
      !Array.isArray(value.routes) || value.routes.length < 1 || value.routes.length > 8) {
    throw new Error("Invalid Harness routing configuration");
  }
  const priced = value.pricing_version !== undefined || value.graphjin_price !== undefined ||
    value.routes.some((route: unknown) => Boolean(route && typeof route === "object" && "price" in route));
  if (priced && (typeof value.pricing_version !== "string" || !value.pricing_version || value.pricing_version.length > 128 ||
      value.pricing_version.trim() !== value.pricing_version)) throw new Error("Invalid Harness pricing version");
  const graphjinPrice = value.graphjin_price === undefined ? undefined : parsePrice(value.graphjin_price);
  const routes: Array<{ key: string; model: string; url: string; api_key_env: string; classifier?: string; price?: HarnessPrice }> = [];
  const providers: string[] = [];
  const modelHosts: Array<{ host: string; port?: number }> = [];
  const keyAliases: Array<{ from: string; to: string }> = [];
  const keys = new Set<string>();
  const credentialNames = new Set<string>();
  const aliasTargets = new Set<string>();
  const hosts = new Set<string>();
  for (const entry of value.routes) {
    if (!entry || typeof entry !== "object" || Array.isArray(entry)) throw new Error("Invalid Harness route");
    const route = entry as Record<string, unknown>;
    if (Object.keys(route).some(key => !["key", "model", "url", "provider", "credential_env", "api_key_env", "classifier", "price"].includes(key)) ||
        typeof route.key !== "string" || !routeKey.test(route.key) || keys.has(route.key) ||
        typeof route.model !== "string" || !route.model || route.model.length > 128 ||
        typeof route.url !== "string" || typeof route.provider !== "string" || !providerName.test(route.provider) ||
        typeof route.credential_env !== "string" || !envName.test(route.credential_env) || credentialNames.has(route.credential_env) ||
        typeof route.api_key_env !== "string" || !harnessKeyEnv.test(route.api_key_env) || aliasTargets.has(route.api_key_env) ||
        (route.classifier !== undefined && !["typesafe-native", "ax-chat-json"].includes(route.classifier as string))) {
      throw new Error("Invalid Harness route");
    }
    if (priced && route.price === undefined) throw new Error("Missing Harness route price");
    const price = route.price === undefined ? undefined : parsePrice(route.price);
    let url: URL;
    try { url = new URL(route.url); }
    catch { throw new Error("Invalid Harness route URL"); }
    if (!["https:", "http:"].includes(url.protocol) || !url.hostname || url.username || url.password || url.search || url.hash) {
      throw new Error("Invalid Harness route URL");
    }
    keys.add(route.key);
    credentialNames.add(route.credential_env);
    aliasTargets.add(route.api_key_env);
    routes.push({ key: route.key, model: route.model, url: route.url, api_key_env: route.api_key_env,
      ...(route.classifier ? {classifier: route.classifier as string} : {}),
      ...(price ? {price} : {}) });
    if (!providers.includes(route.provider)) providers.push(route.provider);
    keyAliases.push({ from: route.credential_env, to: route.api_key_env });
    const endpoint = { host: url.hostname, ...(url.port || url.protocol === "http:" ? { port: Number(url.port || 80) } : {}) };
    const identity = `${endpoint.host}:${endpoint.port ?? 443}`;
    if (!hosts.has(identity)) {
      hosts.add(identity);
      modelHosts.push(endpoint);
    }
  }
  if ([...credentialNames].some(name => aliasTargets.has(name))) {
    throw new Error("Harness credential aliases overlap");
  }
  for (const stage of ["context", "executor", "responder"] as const) {
    if (typeof value[stage] !== "string" || !keys.has(value[stage])) {
      throw new Error("Harness stage has no approved route");
    }
  }
  if (value.skill !== undefined && (typeof value.skill !== "string" || !keys.has(value.skill))) {
    throw new Error("Harness skill stage has no approved route");
  }
  if (value.triage !== undefined && (typeof value.triage !== "string" || !keys.has(value.triage) || !priced ||
      value.triage === value.context || value.triage === value.executor || value.triage === value.responder || value.triage === value.skill)) {
    throw new Error("Harness triage stage requires a dedicated priced route");
  }
  if ((value.triage === undefined) !== (value.budget_policy === undefined)) {
    throw new Error("Harness triage requires a pinned budget policy");
  }
  const budgetPolicy = value.budget_policy === undefined ? undefined : parseBudgetPolicy(value.budget_policy);
  const escalation = value.executor_escalation;
  const afterErrors = value.executor_after_errors;
  if ((escalation === undefined) !== (afterErrors === undefined) ||
      escalation !== undefined && (typeof escalation !== "string" || !keys.has(escalation) ||
        !Number.isInteger(afterErrors) || (afterErrors as number) < 1 || (afterErrors as number) > 8 ||
        escalation === value.executor || escalation === value.triage || value.executor === value.context ||
        value.executor === value.responder || value.executor === value.skill)) {
    throw new Error("Invalid Harness executor escalation");
  }
  const active = new Set([value.context, value.executor, value.responder, value.skill, escalation]);
  const fallbackRows = value.fallbacks;
  if (fallbackRows !== undefined && (!Array.isArray(fallbackRows) || fallbackRows.length > routes.length)) {
    throw new Error("Invalid Harness fallback profile");
  }
  const fallbacks: Array<{from: string; to: string}> = [];
  const fallbackSources = new Set<string>();
  for (const entry of (fallbackRows ?? []) as unknown[]) {
    if (!entry || typeof entry !== "object" || Array.isArray(entry)) throw new Error("Invalid Harness fallback route");
    const pair = entry as Record<string, unknown>;
    if (Object.keys(pair).some(key => !["from", "to"].includes(key)) ||
        typeof pair.from !== "string" || !active.has(pair.from) || fallbackSources.has(pair.from) ||
        typeof pair.to !== "string" || !keys.has(pair.to) || pair.to === value.triage || pair.from === pair.to) {
      throw new Error("Invalid Harness fallback route");
    }
    fallbackSources.add(pair.from);
    fallbacks.push({from: pair.from, to: pair.to});
  }
  return {
    manifest: JSON.stringify({ context: value.context, executor: value.executor, responder: value.responder,
      ...(escalation ? {executor_escalation: escalation, executor_after_errors: afterErrors} : {}),
      ...(value.skill ? { skill: value.skill } : {}), ...(value.triage ? { triage: value.triage, budget_policy: budgetPolicy } : {}),
      ...(fallbacks.length > 0 ? {fallbacks} : {}), routes,
      ...(priced ? {pricing_version: value.pricing_version} : {}),
      ...(graphjinPrice ? {graphjin_price: graphjinPrice} : {}) }),
    providers, modelHosts, keyAliases,
  };
}
