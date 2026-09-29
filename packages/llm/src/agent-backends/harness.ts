import { spawn } from "node:child_process";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { createInterface } from "node:readline";
import { parse } from "yaml";
import type { AgentBackend, AgentEvent, AgentModelIdentity, AgentRunOptions, AgentRunResult, AgentTokenUsage } from "../agent-backend";
import { VENDORED_HARNESS_MODEL_BINARY } from "../agent-runtime-contract";
import { boundedSkillQuery } from "../work/harness-routing";
/** Opt-in read-only M3 backend. Hermes remains the default and keeps its warm pool. */
export class HarnessBackend implements AgentBackend {
    readonly id = "harness" as const;
    readonly capabilities = { mcpTools: false, brokerLookup: true, sessionResume: false, nativeDelegation: "ax-child-agent" } as const;
    constructor(readonly configuredIdentity?: AgentModelIdentity) { }
    get model() { return this.configuredIdentity?.model; }
    async run(opts: AgentRunOptions): Promise<AgentRunResult> {
        const env = { ...process.env };
        const routed = Boolean(env.HARNESS_MODEL_ROUTES);
        const config = (routed ? {} : parse(readFileSync(join(env.HERMES_HOME ?? "", "config.yaml"), "utf8"))) as {
            model?: {
                provider?: string;
                default?: string;
                base_url?: string;
            };
        };
        // The legacy one-route path uses Hermes's OpenAI-compatible config.
        // Host-approved multi-route manifests are parsed and pinned by Go.
        if (!routed && !["openai", "openai-api", "custom"].includes(config.model?.provider ?? "")) {
            throw new Error("Harness M3 requires an OpenAI-compatible model route");
        }
        const lookupRead = opts.mcpBridgeEnv?.OPENNEKO_HARNESS_LOOKUP_READ !== "0";
        if ((lookupRead && (!env.OPENNEKO_BROKER_URL || !env.OPENNEKO_BROKER_TOKEN)) || (!routed && !env.api_key)) {
            throw new Error("Harness M3 requires a scoped broker and OpenShell model placeholder");
        }
        const runId = opts.runId;
        if (!runId || !opts.workspace)
            throw new Error("Harness M3 requires host run identity and workspace");
        const maxOperations = env.OPENNEKO_HARNESS_MAX_OPERATIONS ? Number(env.OPENNEKO_HARNESS_MAX_OPERATIONS) : 4;
        const maxModelCalls = env.OPENNEKO_HARNESS_MAX_MODEL_CALLS ? Number(env.OPENNEKO_HARNESS_MAX_MODEL_CALLS) : 16;
        const maxModelTokens = env.OPENNEKO_HARNESS_MAX_MODEL_TOKENS ? Number(env.OPENNEKO_HARNESS_MAX_MODEL_TOKENS) : 1_000_000;
        const maxCostMicros = env.OPENNEKO_HARNESS_MAX_COST_MICROS ? Number(env.OPENNEKO_HARNESS_MAX_COST_MICROS) : undefined;
        if (!Number.isInteger(maxOperations) || maxOperations < 1 || maxOperations > 32 ||
            !Number.isInteger(maxModelCalls) || maxModelCalls < 1 || maxModelCalls > 64 ||
            !Number.isInteger(maxModelTokens) || maxModelTokens < 1 || maxModelTokens > 1_000_000 ||
            maxCostMicros !== undefined && (!Number.isSafeInteger(maxCostMicros) || maxCostMicros < 1 || maxCostMicros > 1_000_000_000_000))
            throw new Error("Invalid trusted Harness run budget");
        const workflowRunId = opts.mcpBridgeEnv?.OPENNEKO_HARNESS_WORKFLOW_RUN_ID;
        const childReads = opts.nativeDelegation === "disabled" ? "" : opts.mcpBridgeEnv?.OPENNEKO_MCP_MODE === "agent-job"
            ? lookupRead ? "lookup" : "" : workflowRunId ? "lookup" :
            (opts.mcpBridgeEnv?.OPENNEKO_HARNESS_MCP_MEMORY_READ ?? env.OPENNEKO_HARNESS_MCP_MEMORY_READ) === "1" &&
            (opts.mcpBridgeEnv?.OPENNEKO_HARNESS_RECORDS_ONLY ?? env.OPENNEKO_HARNESS_RECORDS_ONLY) !== "1"
            ? "lookup,mcp_memory_search" : "";
        const child = spawn(VENDORED_HARNESS_MODEL_BINARY, [], {
            env: { ...env, ...opts.mcpBridgeEnv, HARNESS_MODEL_URL: config.model?.base_url ?? "", HARNESS_MODEL: config.model?.default ?? "",
                HARNESS_MODEL_API_KEY: env.api_key, OPENNEKO_HARNESS_ACTION_KINDS: env.OPENNEKO_HARNESS_ACTION_KINDS ?? "", HARNESS_STATE_DIR: join(opts.workspace.runRoot, ".harness"),
                OPENNEKO_HARNESS_CHILD_READS: childReads,
                OPENNEKO_HARNESS_UPLOADS_DIR: opts.workspace.threadUploadsRoot,
                OPENNEKO_HARNESS_WORKSPACE_DIR: opts.workspace.artifactRoot },
            stdio: ["pipe", "pipe", "ignore"],
        });
        let killTimer: ReturnType<typeof setTimeout> | undefined;
        const abort = () => { child.kill("SIGTERM"); killTimer ??= setTimeout(() => child.kill("SIGKILL"), 6000); };
        opts.signal?.addEventListener("abort", abort, { once: true });
        const timer = setTimeout(abort, opts.timeoutMs ?? 120000);
        child.on("spawn", () => { if (opts.signal?.aborted)
            abort(); });
        const stop = abort;
        let result: AgentRunResult | undefined;
        let sequence = 0;
        const exit = new Promise<number | null>((resolve, reject) => { child.once("error", reject); child.once("close", resolve); });
        // Attach immediately so process-start failures cannot become unhandled rejections.
        void exit.catch(() => undefined);
        child.stdin.on("error", () => undefined);
        child.stdin.end(JSON.stringify({ version: 1, run_id: runId, input_id: runId,
            max_operations: maxOperations, max_model_calls: maxModelCalls, max_model_tokens: maxModelTokens,
            ...(maxCostMicros ? {max_cost_micros: maxCostMicros} : {}),
            ...(opts.userMessage ? { skill_query: boundedSkillQuery(opts.userMessage) } : {}),
            prompt: opts.userMessage ? `${opts.prompt}\n\nUser request:\n${opts.userMessage}` : opts.prompt }));
        try {
            for await (const line of createInterface({ input: child.stdout })) {
                if (line.length > 3 * 1024 * 1024)
                    throw new Error("Harness event exceeds limit");
                const event = JSON.parse(line);
                if (event.version !== 1 || event.run_id !== runId || event.sequence !== ++sequence || result)
                    throw new Error("Invalid harness event sequence");
                if (event.type === "run.finished") {
                    if (!["completed", "failed", "cancelled"].includes(event.result?.status))
                        throw new Error("Invalid harness result");
                    result = harnessResult(event.result);
                    await opts.onEvent?.({ type: "usage", source: "outer", usage: harnessUsage(event.result?.usage) });
                    const cost = harnessCost(event.result?.cost);
                    if (cost) await opts.onEvent?.({type: "cost", source: "harness", ...cost});
                }
                else if (event.type === "model.stage_usage") {
                    const stage = harnessStageUsage(event);
                    if (stage) await opts.onEvent?.(stage);
                }
                else if (event.type === "tool.started") {
                    const name = event.name === "propose" ? "neko_action_proposal" : event.name === "lookup" ? "neko_graphjin_agent" : event.name === "mcp_memory_search" ? "mcp_neko_memory_search" : event.name;
                    await opts.onEvent?.({ type: "tool_start", id: `harness-operation-${event.operation_id}`, name });
                }
                else if (event.type === "tool.finished") {
                    await opts.onEvent?.({ type: "tool_end", id: `harness-operation-${event.operation_id}`, result: event.data,
                        ...(event.name === "lookup" ? { remoteUsage: harnessRemoteUsage(event.remote_usage) } : {}),
                        ...(event.error ? { error: String(event.error) } : {}) });
                }
                else if (event.type === "child.started") {
                    await opts.onEvent?.({ type: "tool_start", id: `harness-child-${event.span_id}`, name: "ax_child_agent" });
                }
                else if (event.type === "child.finished") {
                    await opts.onEvent?.({ type: "tool_end", id: `harness-child-${event.span_id}` });
                }
                else if (event.type === "run.started") {
                    await opts.onEvent?.({ type: "status", message: "Harness is working…" });
                }
            }
            const code = await exit;
            if (opts.signal?.aborted)
                return { status: "cancelled", finalText: "" };
            if (!result || (result.status === "completed" && code !== 0))
                throw new Error("Harness exited without a valid terminal result");
            if (result.finalText)
                await opts.onEvent?.({ type: "message", role: "assistant", content: result.finalText });
            return result;
        }
        catch (error) {
            stop();
            await exit.catch(() => undefined);
            throw error;
        }
        finally {
            clearTimeout(timer);
            if (killTimer)
                clearTimeout(killTimer);
            opts.signal?.removeEventListener("abort", abort);
        }
    }
}

const HARNESS_STAGES = new Set(["distiller", "executor", "responder", "child.distiller", "child.executor", "child.responder", "unattributed"]);

/** A diagnostic projection of Ax stage usage. It never feeds spend admission. */
export function harnessStageUsage(raw: unknown): Extract<AgentEvent, { type: "stage_usage" }> | undefined {
    if (!raw || typeof raw !== "object") return undefined;
    const event = raw as Record<string, unknown>;
    if (!HARNESS_STAGES.has(String(event.name))) return undefined;
    const value = event.stage_usage;
    if (!value || typeof value !== "object") return undefined;
    const fields = value as Record<string, unknown>;
    const requests = fields.requests;
    const reported = fields.reported ?? 0;
    const coverage = fields.coverage;
    if (!Number.isSafeInteger(requests) || (requests as number) < 1 || (requests as number) > 64 ||
        !Number.isSafeInteger(reported) || (reported as number) < 0 || (reported as number) > (requests as number)) return undefined;
    const expected = reported === requests ? "complete" : reported ? "partial" : "unavailable";
    if (coverage !== expected) return undefined;
    const tokenFields = ["input_tokens", "output_tokens", "total_tokens", "cache_read_tokens", "cache_write_tokens", "reasoning_tokens"] as const;
    if (tokenFields.some(field => fields[field] !== undefined &&
        (!Number.isSafeInteger(fields[field]) || (fields[field] as number) < 0 || (fields[field] as number) > 1_000_000_000_000))) return undefined;
    const usage = harnessUsage(fields);
    if (usage.coverage !== coverage || usage.missingReasons?.some(reason => reason.startsWith("Harness "))) return undefined;
    return { type: "stage_usage", source: "harness", stage: event.name as Extract<AgentEvent, { type: "stage_usage" }>["stage"],
        requests: requests as number, reported: reported as number, usage };
}

/** Accept only the content-free Go receipt; malformed or older events charge conservatively. */
export function harnessRemoteUsage(raw: unknown): Extract<AgentEvent, { type: "tool_end" }>["remoteUsage"] {
    const value = raw && typeof raw === "object" ? raw as Record<string, unknown> : {};
    const positive = (number: unknown): number | undefined =>
        typeof number === "number" && Number.isSafeInteger(number) && number > 0 && number <= 1_000_000_000_000 ? number : undefined;
    const nonnegative = (number: unknown): number | undefined =>
        typeof number === "number" && Number.isSafeInteger(number) && number >= 0 && number <= 1_000_000_000_000 ? number : undefined;
    const chargedTokens = positive(value.charged_tokens);
    if (chargedTokens === undefined || typeof value.reported !== "boolean") return undefined;
    const totalTokens = positive(value.total_tokens);
    if (value.reported && chargedTokens !== totalTokens) return undefined;
    if (!value.reported && (totalTokens !== undefined || chargedTokens !== 12 * 4096)) return undefined;
    const promptTokens = nonnegative(value.prompt_tokens);
    const completionTokens = nonnegative(value.completion_tokens);
    const llmCalls = nonnegative(value.llm_calls);
    return { reported: value.reported, chargedTokens,
        ...(promptTokens !== undefined ? { promptTokens } : {}),
        ...(completionTokens !== undefined ? { completionTokens } : {}),
        ...(totalTokens !== undefined ? { totalTokens } : {}),
        ...(llmCalls !== undefined ? { llmCalls } : {}) };
}

/** Shared by live execution and validated checkpoint adoption. */
export function harnessResult(result: {status: AgentRunResult["status"]; kind?: string; proposals?: {id?:string;status:string}[]; delegations?: unknown[]; usage?: unknown; cost?: unknown; answer?: string; code?: string}): AgentRunResult {
    const outer = harnessUsage(result.usage);
    const cost = harnessCost(result.cost);
    return { backendState: { harness: { version: 1, kind: result.kind, proposals: result.proposals ?? [], delegations: result.delegations ?? [], usageCoverage: outer.coverage, usageScope: "outer-only",
      ...(cost ? {cost} : {}) } }, status: result.status, finalText: result.answer ?? "",
        ...(result.code ? {error: result.code} : {}) };
}

export function harnessCost(raw: unknown): {chargedMicros: number; budgetMicros: number; pricingVersion: string} | undefined {
    if (!raw || typeof raw !== "object" || Array.isArray(raw)) return undefined;
    const cost = raw as Record<string, unknown>;
    if (typeof cost.pricing_version !== "string" || !cost.pricing_version || cost.pricing_version.length > 128 ||
        !Number.isSafeInteger(cost.charged_micros) || (cost.charged_micros as number) < 0 || (cost.charged_micros as number) > 8_000_000_000_000_000 ||
        !Number.isSafeInteger(cost.budget_micros) || (cost.budget_micros as number) < 1 || (cost.budget_micros as number) > 1_000_000_000_000) return undefined;
    return {chargedMicros: cost.charged_micros as number, budgetMicros: cost.budget_micros as number, pricingVersion: cost.pricing_version};
}

export function harnessUsage(raw: unknown): AgentTokenUsage {
    const usage = raw && typeof raw === "object" ? raw as Record<string, unknown> : {};
    const requests = usage.requests;
    const reported = usage.reported;
    const coverage = usage.coverage;
    if (!Number.isSafeInteger(requests) || !Number.isSafeInteger(reported) ||
        (requests as number) < 0 || (requests as number) > 64 || (reported as number) < 0 || (reported as number) > (requests as number) ||
        !["complete", "partial", "unavailable"].includes(String(coverage))) {
        return { coverage: "unavailable", missingReasons: ["Harness omitted valid model usage"] };
    }
    const expectedCoverage = reported === requests && (requests as number) > 0 ? "complete" : (reported as number) > 0 ? "partial" : "unavailable";
    if (coverage !== expectedCoverage) return { coverage: "unavailable", missingReasons: ["Harness reported inconsistent model usage coverage"] };
    const fields = ["input_tokens", "output_tokens", "total_tokens", "cache_read_tokens", "cache_write_tokens", "reasoning_tokens"] as const;
    if ((reported as number) > 0 && fields.slice(0, 3).some(field => usage[field] === undefined) ||
        fields.some(field => usage[field] !== undefined && (!Number.isSafeInteger(usage[field]) || (usage[field] as number) < 0 || (usage[field] as number) > 64_000_000_000_000))) {
        return { coverage: "unavailable", missingReasons: ["Harness reported invalid token counts"] };
    }
    return {
        coverage: coverage as AgentTokenUsage["coverage"],
        ...(reported ? {
            inputTokens: usage.input_tokens as number ?? 0,
            outputTokens: usage.output_tokens as number ?? 0,
            totalTokens: usage.total_tokens as number ?? 0,
            cacheReadTokens: usage.cache_read_tokens as number ?? 0,
            cacheWriteTokens: usage.cache_write_tokens as number ?? 0,
            reasoningTokens: usage.reasoning_tokens as number ?? 0,
        } : {}),
        ...(coverage === "complete" ? {} : { missingReasons: [`Provider usage was available for ${reported} of ${requests} Harness model requests`] }),
    };
}
