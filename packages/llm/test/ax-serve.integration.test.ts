// The Ax backend running a turn in a real `openneko serve`: the model key
// travels out of band, the worker's tools cross serve's socket, and the
// answer comes back as harness events.

import { execFileSync, spawn, type ChildProcess } from "node:child_process";
import { createServer, type Server as HttpServer } from "node:http";
import { mkdir, mkdtemp, readFile, rm } from "node:fs/promises";
import { existsSync } from "node:fs";
import { homedir, tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { afterAll, beforeAll, describe, expect, it, vi } from "vitest";
import { Server } from "@modelcontextprotocol/sdk/server/index.js";
import { CallToolRequestSchema, ListToolsRequestSchema } from "@modelcontextprotocol/sdk/types.js";
import { AxBackend } from "../src/agent-backends/ax";
import { registerServeToolBridge } from "../src/agent-backends/harness-turn";
import { embedTexts } from "../src/embedding";
import { callGateway } from "../src/inference";

function hasGo(): boolean {
  try {
    execFileSync("go", ["version"], { stdio: "ignore" });
    return true;
  } catch {
    return false;
  }
}

const binary = process.env.OPENNEKO_TEST_BINARY;
// Opt in: it builds the openneko binary unless OPENNEKO_TEST_BINARY names one,
// and the sandbox recipe unless OPENNEKO_TEST_SANDBOX_BINARY names one.
const runnable = Boolean(binary) || (process.env.OPENNEKO_SERVE_IT === "1" && hasGo());

(runnable ? describe : describe.skip)("Ax backend in openneko serve", () => {
  let dir: string;
  let serve: ChildProcess;
  let sandbox: ChildProcess;
  let responses: string[];
  let calls = 0;
  let model: HttpServer;
  let modelUrl: string;
  const keys: string[] = [];
  const toolLists: string[][] = [];

  beforeAll(async () => {
    dir = await mkdtemp(join(tmpdir(), "neko-serve-"));
    let bin = binary;
    if (!bin) {
      bin = join(dir, "openneko");
      execFileSync("go", ["build", "-o", bin, "./cmd/openneko"], {
        cwd: resolve(__dirname, "../../../apps/openneko"),
        stdio: "inherit",
      });
    }
    let sandboxBin = process.env.OPENNEKO_TEST_SANDBOX_BINARY;
    if (!sandboxBin) {
      sandboxBin = join(dir, "openneko-sandbox");
      execFileSync("go", ["build", "-o", sandboxBin, "."], {
        cwd: resolve(__dirname, "../../../recipes/sandbox"),
        env: { ...process.env, HOME: homedir() },
        stdio: "inherit",
      });
    }
    responses = [
      `{"javascriptCode":"final('Find reference', {})"}`,
      `{"javascriptCode":"final('Report', {reference:'REF-42'});"}`,
      "Answer: REF-42",
    ];
    model = createServer((req, res) => {
      let body = "";
      req.on("data", (c) => (body += c));
      req.on("end", () => {
        keys.push(String(req.headers.authorization));
        const content = responses[Math.min(calls++, responses.length - 1)];
        res.setHeader("content-type", "application/json");
        res.end(JSON.stringify({ choices: [{ message: { role: "assistant", content }, finish_reason: "stop" }] }));
      });
    });
    await new Promise<void>((done) => model.listen(0, "127.0.0.1", done));
    modelUrl = `http://127.0.0.1:${(model.address() as { port: number }).port}`;

    const sandboxSocket = join(dir, "sandbox.sock");
    sandbox = spawn(sandboxBin, ["--listen", `unix://${sandboxSocket}`], {
      stdio: ["ignore", "ignore", "pipe"],
      env: {
        PATH: process.env.PATH ?? "/usr/bin:/bin",
        OPENNEKO_SANDBOX_WORKSPACES: dir,
        OPENNEKO_SANDBOX_HOMES: join(dir, "sandbox-homes"),
        OPENNEKO_SANDBOX_KEY: "sandbox-key",
      },
    });
    const socket = join(dir, "serve.sock");
    const serveEnv = Object.fromEntries(
      Object.entries(process.env).filter(
        (entry): entry is [string, string] =>
          typeof entry[1] === "string" && /^(PATH|HOME|XDG_CONFIG_HOME|NEKO_PG_[A-Z]+|OPENNEKO_PG_ENV_OVERRIDE)$/.test(entry[0]),
      ),
    );
    serve = spawn(
      bin,
      [
        "serve",
        "--agent-socket", socket,
        "--addr", "127.0.0.1:0",
        "--files-dir", join(dir, "files"),
        "--index-dir", join(dir, "file-index"),
        "--sandbox-url", `unix://${sandboxSocket}`,
      ],
      { stdio: ["ignore", "ignore", "pipe"], env: { ...serveEnv, OPENNEKO_SANDBOX_KEY: "sandbox-key" } },
    );
    for (let i = 0; i < 200 && !existsSync(socket); i++) await new Promise((r) => setTimeout(r, 25));
    if (!existsSync(socket)) throw new Error("openneko serve did not start");
    vi.stubEnv("OPENNEKO_SERVE_SOCKET", socket);
    vi.stubEnv("OPENAI_API_KEY", "org-stored-key");

    registerServeToolBridge(async (servers, env) => {
      const server = new Server({ name: "worker", version: "1" }, { capabilities: { tools: {} } });
      server.setRequestHandler(ListToolsRequestSchema, async () => {
        toolLists.push(servers);
        expect(env.OPENNEKO_MCP_ORG_ID).toBe("org-1");
        return { tools: [{ name: "memory_search", description: "Search memory", inputSchema: { type: "object" } }] };
      });
      server.setRequestHandler(CallToolRequestSchema, async () => ({ content: [] }));
      return server;
    });
  }, 300_000);

  afterAll(async () => {
    registerServeToolBridge(undefined);
    vi.unstubAllEnvs();
    serve?.kill("SIGTERM");
    sandbox?.kill("SIGTERM");
    await new Promise<void>((done) => (model ? model.close(() => done()) : done()));
    if (dir) await rm(dir, { recursive: true, force: true });
  });

  const workspaceIn = async (root: string, run: string) => {
    await mkdir(join(root, "skills"), { recursive: true });
    await mkdir(join(root, "runs", run), { recursive: true });
    return {
      orgRoot: root,
      skillsRoot: join(root, "skills"),
      memoryRoot: join(root, "memory"),
      knowledgeRoot: join(root, "knowledge"),
      uploadsRoot: join(root, "uploads"),
      runsRoot: join(root, "runs"),
      runRoot: join(root, "runs", run),
      artifactRoot: join(root, "runs", run, "artifacts"),
      binRoot: join(root, "runs", run, "bin"),
    };
  };

  it("runs the terminal's command in the sandbox, without the model key", async () => {
    const workspace = await workspaceIn(await mkdtemp(join(dir, "ws-")), "r2");
    vi.stubEnv("OPENNEKO_SANDBOX_URL", "unix:///set-by-serve-only");
    vi.stubEnv("RUN_DIR", workspace.runRoot);
    calls = 0;
    responses = [
      `{"javascriptCode":"final('Run the check', {})"}`,
      `{"javascriptCode":"const r = await terminal({command:'env > \\"$RUN_DIR/env.txt\\"; echo sandboxed'}); console.log(r.output)"}`,
      `{"javascriptCode":"final('Report', {reference:'SBX-1'});"}`,
      "Answer: SBX-1",
    ];
    const backend = new AxBackend({
      route: { provider: "openai-compatible", model: "fixture", url: modelUrl, keyEnv: "OPENAI_API_KEY" },
    });
    const result = await backend.run({
      prompt: "Run the check",
      orgId: "org-1",
      tag: "run-serve-2",
      workspace,
      mcpServers: {},
      mcpBridgeEnv: { OPENNEKO_MCP_ORG_ID: "org-1", OPENNEKO_MCP_THREAD_ID: "thread-2", OPENNEKO_MCP_RUN_ID: "run-serve-2" },
      nativeDelegation: "disabled",
    });
    vi.unstubAllEnvs();
    vi.stubEnv("OPENNEKO_SERVE_SOCKET", join(dir, "serve.sock"));
    vi.stubEnv("OPENAI_API_KEY", "org-stored-key");
    expect(result.status, result.error).toBe("completed");
    const env = await readFile(join(workspace.runRoot, "env.txt"), "utf8");
    expect(env).toContain(`RUN_DIR=${workspace.runRoot}`);
    expect(env).not.toContain("org-stored-key");
    expect(env).not.toContain("sandbox-key");
    expect(env).not.toMatch(/OPENNEKO_(BROKER|SANDBOX|MCP)_/);
  });

  it("answers, with the key and tools crossing the socket", async () => {
    calls = 0;
    responses = [
      `{"javascriptCode":"final('Find reference', {})"}`,
      `{"javascriptCode":"final('Report', {reference:'REF-42'});"}`,
      "Answer: REF-42",
    ];
    keys.length = 0;
    const backend = new AxBackend({
      route: { provider: "openai-compatible", model: "fixture", url: modelUrl, keyEnv: "OPENAI_API_KEY" },
    });
    const workspace = await mkdtemp(join(dir, "ws-"));
    await mkdir(join(workspace, "skills"), { recursive: true });
    const result = await backend.run({
      prompt: "Find reference",
      orgId: "org-1",
      tag: "run-serve-1",
      workspace: {
        orgRoot: workspace,
        skillsRoot: join(workspace, "skills"),
        memoryRoot: join(workspace, "memory"),
        knowledgeRoot: join(workspace, "knowledge"),
        uploadsRoot: join(workspace, "uploads"),
        runsRoot: join(workspace, "runs"),
        runRoot: join(workspace, "runs", "r1"),
        artifactRoot: join(workspace, "runs", "r1", "artifacts"),
        binRoot: join(workspace, "runs", "r1", "bin"),
      },
      mcpServers: { neko_memory: {} },
      mcpBridgeEnv: { OPENNEKO_MCP_ORG_ID: "org-1", OPENNEKO_MCP_THREAD_ID: "thread-1", OPENNEKO_MCP_RUN_ID: "run-serve-1" },
      nativeDelegation: "disabled",
    });
    expect(result.status, result.error).toBe("completed");
    expect(result.finalText).toContain("REF-42");
    expect(keys.length).toBeGreaterThan(0);
    expect(new Set(keys)).toEqual(new Set(["Bearer org-stored-key"]));
    expect(toolLists).toEqual([["neko_memory"]]);
  });

  it("answers for a provider with no key, such as Ollama", async () => {
    calls = 0;
    responses = [
      `{"javascriptCode":"final('Find reference', {})"}`,
      `{"javascriptCode":"final('Report', {reference:'REF-7'});"}`,
      "Answer: REF-7",
    ];
    keys.length = 0;
    vi.stubEnv("MODEL_API_KEY", "");
    const backend = new AxBackend({ route: { provider: "openai-compatible", model: "fixture", url: modelUrl } });
    const workspace = await workspaceIn(await mkdtemp(join(dir, "ws-")), "r3");
    const result = await backend.run({
      prompt: "Find reference",
      orgId: "org-1",
      tag: "run-serve-3",
      workspace,
      mcpServers: {},
      mcpBridgeEnv: { OPENNEKO_MCP_ORG_ID: "org-1", OPENNEKO_MCP_THREAD_ID: "thread-3", OPENNEKO_MCP_RUN_ID: "run-serve-3" },
      nativeDelegation: "disabled",
    });
    expect(result.status, result.error).toBe("completed");
    expect(result.finalText).toContain("REF-7");
    expect(new Set(keys)).toEqual(new Set(["Bearer none"]));
  });

  it("runs a host model call through serve's gateway with the caller's key", async () => {
    calls = 0;
    responses = ["Answer: 42"];
    keys.length = 0;
    const result = await callGateway(
      {
        route: { provider: "openai-compatible", model: "fixture", url: modelUrl },
        signature: `question:string "a question" -> answer:string "the answer"`,
        instruction: "Answer the question.",
        values: { question: "six times seven" },
      },
      "host-call-key",
    );
    expect(result.outputs).toEqual({ answer: "42" });
    expect(result.usage.requests).toBe(1);
    expect(keys).toEqual(["Bearer host-call-key"]);
  });
  it("embeds through serve, with the key in the header only", async () => {
    const seen: Array<{ auth: string; body: { input: string[]; dimensions?: number } }> = [];
    const embedder = createServer((req, res) => {
      let body = "";
      req.on("data", (c) => (body += c));
      req.on("end", () => {
        const parsed = JSON.parse(body) as { input: string[]; dimensions?: number };
        seen.push({ auth: String(req.headers.authorization), body: parsed });
        res.setHeader("content-type", "application/json");
        res.end(JSON.stringify({ data: parsed.input.map((_, index) => ({ index, embedding: [index, 0.5, 0.25] })) }));
      });
    });
    await new Promise<void>((done) => embedder.listen(0, "127.0.0.1", done));
    try {
      vi.stubEnv("NEKO_EMBEDDING_URL", `http://127.0.0.1:${(embedder.address() as { port: number }).port}/v1`);
      vi.stubEnv("NEKO_EMBEDDING_MODEL", "fixture-embed");
      vi.stubEnv("NEKO_EMBEDDING_API_KEY", "embed-key-0451");
      vi.stubEnv("NEKO_EMBEDDING_DIMENSIONS", "3");
      expect(await embedTexts(["first", "second"])).toEqual([[0, 0.5, 0.25], [1, 0.5, 0.25]]);
      expect(seen).toEqual([{ auth: "Bearer embed-key-0451", body: { model: "fixture-embed", input: ["first", "second"], encoding_format: "float", dimensions: 3 } }]);
    } finally {
      await new Promise<void>((done) => embedder.close(() => done()));
    }
  });
});
