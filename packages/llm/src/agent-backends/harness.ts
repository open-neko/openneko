import { spawn } from "node:child_process";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { createInterface } from "node:readline";
import { parse } from "yaml";
import type { AgentBackend, AgentModelIdentity, AgentRunOptions, AgentRunResult } from "../agent-backend";
import { VENDORED_HARNESS_MODEL_BINARY } from "../agent-runtime-contract";
/** Opt-in read-only M3 backend. Hermes remains the default and keeps its warm pool. */
export class HarnessBackend implements AgentBackend {
    readonly id = "harness" as const;
    readonly capabilities = { mcpTools: false, brokerLookup: true, sessionResume: false } as const;
    constructor(readonly configuredIdentity?: AgentModelIdentity) { }
    get model() { return this.configuredIdentity?.model; }
    async run(opts: AgentRunOptions): Promise<AgentRunResult> {
        const env = { ...process.env };
        const config = parse(readFileSync(join(env.HERMES_HOME ?? "", "config.yaml"), "utf8")) as {
            model?: {
                provider?: string;
                default?: string;
                base_url?: string;
            };
        };
        // First approved route is OpenAI-compatible; never reinterpret native Anthropic/Gemini.
        if (!["openai", "openai-api", "custom"].includes(config.model?.provider ?? "")) {
            throw new Error("Harness M3 requires an OpenAI-compatible model route");
        }
        if (!env.OPENNEKO_BROKER_URL || !env.OPENNEKO_BROKER_TOKEN || !env.api_key) {
            throw new Error("Harness M3 requires a scoped broker and OpenShell model placeholder");
        }
        const runId = opts.runId;
        if (!runId || !opts.workspace)
            throw new Error("Harness M3 requires host run identity and workspace");
        const child = spawn(VENDORED_HARNESS_MODEL_BINARY, [], {
            env: { ...env, HARNESS_MODEL_URL: config.model?.base_url ?? "", HARNESS_MODEL: config.model?.default ?? "",
                HARNESS_MODEL_API_KEY: env.api_key, HARNESS_STATE_DIR: join(opts.workspace.runRoot, ".harness") },
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
        child.stdin.end(JSON.stringify({ version: 1, run_id: runId, input_id: runId, prompt: opts.userMessage ? `${opts.prompt}\n\nUser request:\n${opts.userMessage}` : opts.prompt }));
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
                }
                else if (event.type === "tool.started") {
                    await opts.onEvent?.({ type: "tool_start", id: `harness-lookup-${event.operation_id}`, name: "neko_graphjin_agent" });
                }
                else if (event.type === "tool.finished") {
                    await opts.onEvent?.({ type: "tool_end", id: `harness-lookup-${event.operation_id}`, result: event.data, ...(event.error ? { error: String(event.error) } : {}) });
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

/** Shared by live execution and validated checkpoint adoption. */
export function harnessResult(result: {status: AgentRunResult["status"]; kind?: string; delegations?: unknown[]; answer?: string; code?: string}): AgentRunResult {
    return { backendState: { harness: { version: 1, kind: result.kind, delegations: result.delegations ?? [], usageCoverage: "delegated-only" } }, status: result.status, finalText: result.answer ?? "",
        ...(result.code ? {error: result.code} : {}) };
}
