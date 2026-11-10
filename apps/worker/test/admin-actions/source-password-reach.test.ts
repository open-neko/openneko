import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { createServer, type Server } from "node:http";
import type { AddressInfo } from "node:net";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { InMemoryTransport } from "@modelcontextprotocol/sdk/inMemory.js";
import { afterAll, beforeAll, describe, expect, it, vi } from "vitest";
import {
  createTestOrg,
  dbReachable,
  deleteTestOrg,
  uniqueOrgId,
} from "@neko/db/test-helpers";
import {
  action_policy,
  action_request,
  data_source,
  data_source_secret,
  db,
  eq,
  GRAPHJIN_CONFIG_SCOPE,
  llm_provider_config,
  pool,
  work_message,
  work_run,
  work_run_event,
  work_thread,
} from "@neko/db";
import type { AgentEvent } from "@neko/llm";
import { maybeEncryptSecret } from "@neko/llm/secrets";
import { appendWorkRunEvent, runChatTurn } from "@neko/llm/work";
import { MemoryObservationSink, createHarnessObserver } from "@neko/telemetry";
import { createOtlpTelemetryRuntime } from "@neko/telemetry/node";
import { registerSourceConfigAdminAdapter } from "../../src/admin-actions/adapters.js";
import { runActionExecute } from "../../src/jobs/action-execute.js";

const reachable = await dbReachable();
const describeIfDb = reachable ? describe : describe.skip;

const CUSTOMER = "http://customer-gj:8080/api/v1/graphql";
const PASSWORD = "Pl4nted-3b-pw";
const SECRET_NAME = "warehouse-password";

type Captured = {
  modelInput: string[];
  logs: string[];
  traces: Buffer[];
  observations: string;
  stored: string;
  graphjinBodies: string[];
};

function fakeCustomerGraphjin(captured: Captured) {
  const realFetch = globalThis.fetch;
  let previews = 0;
  return vi.fn(async (url: string | URL | Request, init?: RequestInit) => {
    if (String(url) !== CUSTOMER) return realFetch(url, init);
    const body = String(init?.body ?? "");
    captured.graphjinBodies.push(body);
    const echo = body.includes(PASSWORD) ? ` (password ${PASSWORD})` : "";
    let payload: unknown;
    if (body.includes('mode: \\"preview\\"') || body.includes('mode: "preview"')) {
      previews += 1;
      payload =
        previews === 1
          ? { data: null, errors: [{ message: `connection failed${echo}` }] }
          : {
              data: {
                gj_config: {
                  valid: true,
                  preview_id: "pv1",
                  change_summary_json: JSON.stringify([`added source warehouse${echo}`]),
                  findings_json: JSON.stringify([`checked login${echo}`]),
                  errors_json: "[]",
                },
              },
            };
    } else if (body.includes('mode: \\"apply\\"') || body.includes('mode: "apply"')) {
      payload = { data: null, errors: [{ message: `apply failed${echo}` }] };
    } else {
      payload = {
        data: {
          gj_config: {
            catalog_revision: "rev1",
            sources: [{ name: "adventureworks", kind: "database", read_only: true }],
          },
        },
      };
    }
    return new Response(JSON.stringify(payload), {
      status: 200,
      headers: { "content-type": "application/json" },
    });
  });
}

type NekoMcpServer = { type: "sdk"; instance: { connect(transport: unknown): Promise<void> } };

async function callTool(server: NekoMcpServer, name: string, args: Record<string, unknown>) {
  const [clientTransport, serverTransport] = InMemoryTransport.createLinkedPair();
  await server.instance.connect(serverTransport);
  const client = new Client({ name: "fake-model", version: "1.0.0" });
  await client.connect(clientTransport);
  try {
    const result = await client.callTool({ name, arguments: args });
    return (result.content as Array<{ text: string }>).map((item) => item.text).join("\n");
  } finally {
    await client.close();
  }
}

describeIfDb("the database password stays out of the model, logs, traces and thread history", () => {
  const orgId = uniqueOrgId("pw-reach");
  const captured: Captured = { modelInput: [], logs: [], traces: [], observations: "", stored: "", graphjinBodies: [] };
  let configRoot = "";
  let collector: Server;
  const saved = {
    config: process.env.OPENNEKO_GRAPHJIN_CONFIG,
    socket: process.env.OPENNEKO_CONFIG_CHECK_SOCKET,
  };

  beforeAll(async () => {
    registerSourceConfigAdminAdapter();
    configRoot = await mkdtemp(join(tmpdir(), "openneko-pw-reach-"));
    const configFile = join(configRoot, "agentic.yml");
    await writeFile(
      configFile,
      "production: true\nsources:\n  - name: adventureworks\n    kind: database\n",
    );
    process.env.OPENNEKO_GRAPHJIN_CONFIG = configFile;
    delete process.env.OPENNEKO_CONFIG_CHECK_SOCKET;
    collector = createServer((req, res) => {
      const chunks: Buffer[] = [];
      req.on("data", (chunk: Buffer) => chunks.push(chunk));
      req.on("end", () => {
        captured.traces.push(Buffer.concat(chunks));
        res.writeHead(200, { "content-type": "application/x-protobuf" });
        res.end();
      });
    });
    await new Promise<void>((resolve) => collector.listen(0, "127.0.0.1", resolve));
    await createTestOrg(orgId);
    await db().insert(llm_provider_config).values({
      org_id: orgId,
      scope: GRAPHJIN_CONFIG_SCOPE,
      provider: "graphjin-config",
      enabled: true,
      config: { sourceConfigEnabled: true },
      secrets: {},
    });
    await db().insert(data_source).values({
      org_id: orgId,
      kind: "graphjin",
      graphql_url: CUSTOMER,
      name: "default",
      is_default: true,
    });
    await db().insert(data_source_secret).values({
      org_id: orgId,
      name: SECRET_NAME,
      value_enc: maybeEncryptSecret(PASSWORD),
      updated_at: new Date(),
    });
    await db().insert(action_policy).values({
      org_id: orgId,
      name: "source config needs approval",
      applies_to_kinds: ["source_config_admin"],
      mode: "approval_required",
    });
    await runFlow();
  }, 60_000);

  afterAll(async () => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
    await deleteTestOrg(orgId);
    await pool().end();
    await new Promise<void>((resolve) => collector.close(() => resolve()));
    await rm(configRoot, { recursive: true, force: true });
    for (const [key, name] of [
      ["config", "OPENNEKO_GRAPHJIN_CONFIG"],
      ["socket", "OPENNEKO_CONFIG_CHECK_SOCKET"],
    ] as const) {
      if (saved[key] === undefined) delete process.env[name];
      else process.env[name] = saved[key];
    }
  });

  async function runFlow() {
    vi.stubGlobal("fetch", fakeCustomerGraphjin(captured));
    for (const method of ["log", "info", "warn", "error", "debug"] as const) {
      const original = console[method];
      vi.spyOn(console, method).mockImplementation((...args: unknown[]) => {
        captured.logs.push(args.map((arg) => (typeof arg === "string" ? arg : JSON.stringify(arg))).join(" "));
        void original;
      });
    }
    for (const stream of [process.stdout, process.stderr]) {
      const write = stream.write.bind(stream);
      vi.spyOn(stream, "write").mockImplementation(((chunk: unknown, ...rest: unknown[]) => {
        captured.logs.push(Buffer.isBuffer(chunk) ? chunk.toString("utf8") : String(chunk));
        return (write as (...args: unknown[]) => boolean)(chunk, ...rest);
      }) as typeof stream.write);
    }

    const otlp = createOtlpTelemetryRuntime({
      serviceName: "openneko-worker-test",
      endpoint: `http://127.0.0.1:${(collector.address() as AddressInfo).port}/v1/traces`,
    });
    const memory = new MemoryObservationSink();
    const [thread] = await db().insert(work_thread).values({ org_id: orgId, title: "Connect the warehouse" }).returning();
    const turn = async (message: string, backendRun: (opts: Record<string, unknown>) => Promise<unknown>) => {
      const [run] = await db()
        .insert(work_run)
        .values({ org_id: orgId, thread_id: thread!.id, backend: "ax", status: "queued", actor_role: "admin" })
        .returning();
      const observer = createHarnessObserver({ runId: run!.id, sinks: [memory, otlp.sink] });
      await runChatTurn(
        {
          orgId,
          threadId: thread!.id,
          runId: run!.id,
          message,
          channel: "web",
          observer,
          emit: async (event) => {
            await appendWorkRunEvent({ orgId, threadId: thread!.id, runId: run!.id, event });
          },
        },
        {
          resolveAgentBackend: vi.fn(async () => ({
            id: "ax" as const,
            model: "fake-model",
            capabilities: { mcpTools: true, sessionResume: false, nativeDelegation: "ax-worker" as const },
            run: vi.fn(backendRun),
          })),
          ensureWorkWorkspace: vi.fn(async () => ({
            orgRoot: join(configRoot, "org"),
            skillsRoot: join(configRoot, "skills"),
            memoryRoot: join(configRoot, "memory"),
            knowledgeRoot: join(configRoot, "knowledge"),
            uploadsRoot: join(configRoot, "uploads"),
            runsRoot: join(configRoot, "runs"),
            threadUploadsRoot: join(configRoot, "uploads", "t"),
            runRoot: join(configRoot, "runs", "r"),
            artifactRoot: join(configRoot, "runs", "r", "artifacts"),
            binRoot: join(configRoot, "runs", "r", "bin"),
          })),
          formatWorkMemoryPromptContext: vi.fn(async () => ""),
          listInstalledSkills: vi.fn(async () => []),
          prefetchKnowledgeForOrg: vi.fn(async () => ({ ok: true as const, files: [], mode: "legacy" as const })),
        },
      );
      return run!.id;
    };

    const proposal = {
      action: "register_source",
      name: "warehouse",
      kind: "database",
      type: "postgres",
      host: "warehouse-db",
      port: 5432,
      dbname: "warehouse",
      user: "reader",
      secretRef: SECRET_NAME,
      intent: "Connect the warehouse database",
    };
    const firstRun = await turn("Connect the warehouse database", async (opts) => {
      captured.modelInput.push(String(opts.prompt), String(opts.userMessage ?? ""));
      const onEvent = opts.onEvent as (event: AgentEvent) => Promise<void>;
      const servers = opts.mcpServers as Record<string, NekoMcpServer>;
      expect(Object.keys(servers)).toContain("neko_source_config_manager");
      for (const attempt of [1, 2]) {
        const id = `ax-op-${attempt}`;
        await onEvent({ type: "tool_start", id, name: "mcp_neko_source_config_manager_request_source_config_change", input: proposal });
        const result = await callTool(servers.neko_source_config_manager!, "request_source_config_change", proposal);
        captured.modelInput.push(result);
        await onEvent({ type: "tool_end", id, result });
      }
      await onEvent({ type: "message", role: "assistant", content: "The proposal is waiting for approval." });
      return { finalText: "The proposal is waiting for approval.", status: "completed", backendState: {} };
    });

    await turn("Is it connected?", async (opts) => {
      captured.modelInput.push(String(opts.prompt), String(opts.userMessage ?? ""));
      return { finalText: "Not yet.", status: "completed", backendState: {} };
    });

    const [request] = await db()
      .select()
      .from(action_request)
      .where(eq(action_request.org_id, orgId));
    expect(request?.kind).toBe("source_config_admin");
    expect(request?.work_run_id).toBe(firstRun);
    await db().update(action_request).set({ status: "approved" }).where(eq(action_request.id, request!.id));
    await runActionExecute({ orgId, actionRequestId: request!.id });

    await otlp.forceFlush();
    await otlp.shutdown();
    vi.restoreAllMocks();
    vi.unstubAllGlobals();

    captured.observations = JSON.stringify(memory.observations);
    captured.stored = JSON.stringify({
      events: await db().select({ payload: work_run_event.payload }).from(work_run_event).where(eq(work_run_event.org_id, orgId)),
      messages: await db().select({ content: work_message.content }).from(work_message).where(eq(work_message.org_id, orgId)),
      threads: await db().select({ state: work_thread.backend_state }).from(work_thread).where(eq(work_thread.org_id, orgId)),
      runs: await db().select({ error: work_run.error }).from(work_run).where(eq(work_run.org_id, orgId)),
      requests: await db().select().from(action_request).where(eq(action_request.org_id, orgId)),
    });
  }

  it("reaches GraphJin, so the planted value is live", () => {
    expect(captured.graphjinBodies.some((body) => body.includes(PASSWORD))).toBe(true);
  });

  it("never reaches the model", () => {
    const seen = captured.modelInput.join("\n");
    expect(seen).toContain("connection failed (password [REDACTED])");
    expect(seen).toContain('"decision":"pending_approval"');
    expect(seen).not.toContain(PASSWORD);
  });

  it("never reaches a log line", () => {
    const logs = captured.logs.join("\n");
    expect(logs).toContain("apply failed (password [REDACTED])");
    expect(logs).not.toContain(PASSWORD);
  });

  it("never reaches a trace span", () => {
    expect(captured.traces.length).toBeGreaterThan(0);
    expect(Buffer.concat(captured.traces).includes(Buffer.from(PASSWORD))).toBe(false);
    expect(captured.observations).toContain("tool.end");
    expect(captured.observations).not.toContain(PASSWORD);
  });

  it("never reaches the stored thread history", () => {
    expect(captured.stored).toContain("action_request_result");
    expect(captured.stored).toContain("apply failed (password [REDACTED])");
    expect(captured.stored).toContain("added source warehouse (password [REDACTED])");
    expect(captured.stored).not.toContain(PASSWORD);
  });
});
