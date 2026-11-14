import { existsSync } from "node:fs";
import { mkdir, rm, stat } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import pg from "pg";
import { afterAll, describe, expect, it } from "vitest";
import { app_user, db } from "@neko/db";
import { createTestOrg, dbReachable, seedProvider } from "@neko/db/test-helpers";
import { maybeEncryptSecret } from "@neko/llm/secrets";
import { ensureOrgWorkspace, rememberWorkMemory, writeWorkSkill } from "@neko/llm/work";
import {
  EVAL_EPISODE_DATABASE_PREFIX,
  evalEpisodeFolderPrefix,
  evalServerKey,
  EvalInstallHost,
  availableMemoryBytes,
  evalDatabaseLoginFromEnv,
  evalMemoryReserveFromEnv,
  evalSessionCookie,
  parallelEpisodeCap,
  waitForEpisodeMemory,
} from "../scripts/eval-install";
import {
  RunEventFeed,
  ScriptedModelServer,
  ScriptedTurn,
  mcpToolNames,
  scriptedRequestKind,
  skillsRootFromPrompt,
} from "../scripts/eval-scripted-model";

const GB = 1024 ** 3;

describe("eval install helpers", () => {
  it("keys the episode folders to one Postgres server", () => {
    const login = { host: "127.0.0.1", port: 15432 };
    expect(evalEpisodeFolderPrefix(login)).toMatch(/^openneko-eval-ep-[0-9a-f]{12}-$/);
    expect(evalEpisodeFolderPrefix(login)).toBe(evalEpisodeFolderPrefix({ ...login }));
    expect(evalEpisodeFolderPrefix({ ...login, port: 55432 })).not.toBe(evalEpisodeFolderPrefix(login));
    expect(evalServerKey({ ...login, host: "db" })).not.toBe(evalServerKey(login));
  });

  it("keeps 2 GB free when it caps parallel episodes", () => {
    expect(availableMemoryBytes("MemTotal: 16 kB\nMemAvailable:    8388608 kB\n")).toBe(8 * GB);
    expect(availableMemoryBytes("MemTotal: 16 kB\n")).toBeUndefined();
    expect(parallelEpisodeCap(8 * GB)).toBe(6);
    expect(parallelEpisodeCap(3 * GB)).toBe(1);
    expect(parallelEpisodeCap(2.5 * GB)).toBe(0);
    expect(parallelEpisodeCap(8 * GB, 4 * GB)).toBe(4);
    expect(parallelEpisodeCap(undefined)).toBe(1);
  });

  it("reads the memory reserve from the environment", () => {
    expect(evalMemoryReserveFromEnv({})).toBe(2 * GB);
    expect(evalMemoryReserveFromEnv({ OPENNEKO_EVAL_MEMORY_RESERVE_GB: "" })).toBe(2 * GB);
    expect(evalMemoryReserveFromEnv({ OPENNEKO_EVAL_MEMORY_RESERVE_GB: "0.5" })).toBe(0.5 * GB);
    expect(() => evalMemoryReserveFromEnv({ OPENNEKO_EVAL_MEMORY_RESERVE_GB: "lots" })).toThrow(/not a number/);
    expect(() => evalMemoryReserveFromEnv({ OPENNEKO_EVAL_MEMORY_RESERVE_GB: "-1" })).toThrow(/not a number/);
  });

  describe("the wait for free memory", () => {
    const meminfo = (gb: number) => `MemAvailable: ${gb * 1024 * 1024} kB\n`;
    const reader = (values: number[]) => {
      let reads = 0;
      const read = async () => meminfo(values[Math.min(reads++, values.length - 1)]!);
      return { read, reads: () => reads };
    };

    it("starts at once when memory is free, with no log line", async () => {
      const memory = reader([3]);
      const slept: number[] = [];
      const lines: string[] = [];
      const cap = await waitForEpisodeMemory({
        readMeminfo: memory.read,
        reserve: 2 * GB,
        sleep: async (ms) => void slept.push(ms),
        log: (line) => lines.push(line),
      });
      expect(cap).toBe(1);
      expect(slept).toEqual([]);
      expect(lines).toEqual([]);
    });

    it("checks each 15 s and logs one line until memory is free", async () => {
      const memory = reader([1, 2, 2.5, 4]);
      const slept: number[] = [];
      const lines: string[] = [];
      const cap = await waitForEpisodeMemory({
        readMeminfo: memory.read,
        reserve: 2 * GB,
        sleep: async (ms) => void slept.push(ms),
        log: (line) => lines.push(line),
      });
      expect(cap).toBe(2);
      expect(memory.reads()).toBe(4);
      expect(slept).toEqual([15_000, 15_000, 15_000]);
      expect(lines).toHaveLength(1);
      expect(lines[0]).toContain("3 GB");
      expect(lines[0]).toContain("15 min");
    });

    it("fails only after 15 minutes", async () => {
      const memory = reader([1]);
      const slept: number[] = [];
      const lines: string[] = [];
      await expect(
        waitForEpisodeMemory({
          readMeminfo: memory.read,
          reserve: 2 * GB,
          sleep: async (ms) => void slept.push(ms),
          log: (line) => lines.push(line),
        }),
      ).rejects.toThrow("needs 3 GB of free memory to start an episode; it waited 900 s");
      expect(slept).toHaveLength(60);
      expect(slept.reduce((sum, ms) => sum + ms, 0)).toBe(15 * 60_000);
      expect(memory.reads()).toBe(61);
      expect(lines).toHaveLength(1);
    });

    it("uses the reserve it is given", async () => {
      const memory = reader([3]);
      await expect(
        waitForEpisodeMemory({ readMeminfo: memory.read, reserve: 4 * GB, waitMs: 0, log: () => {} }),
      ).rejects.toThrow("needs 5 GB");
    });
  });

  it("signs a session cookie the way web checks it", () => {
    const cookie = evalSessionCookie("s".repeat(40), "member-1", 1_000_000);
    const [name, value] = cookie.split("=");
    expect(name).toBe("openneko_session");
    const [user, expires, mac] = value!.split(".");
    expect(user).toBe("member-1");
    expect(Number(expires)).toBe(1000 + 12 * 3600);
    expect(mac).toMatch(/^[A-Za-z0-9_-]{43}$/);
  });
});

describe("scripted model protocol", () => {
  it("tells the four Ax calls apart", () => {
    const tools = [{ type: "function" }];
    expect(scriptedRequestKind({ messages: [{ content: "<task_definition>\n## Distiller\n" }], tools })).toBe("distiller");
    expect(scriptedRequestKind({ messages: [{ content: "Answer using the supplied context." }], tools })).toBe("executor");
    expect(scriptedRequestKind({ messages: [{ content: "## Answer Synthesis Agent" }] })).toBe("responder");
    expect(
      scriptedRequestKind({ messages: [{ content: "Each guide is a written method for one kind of task." }] }),
    ).toBe("rerank");
  });

  it("reads tool names and the skills folder from the prompt", () => {
    const prompt =
      "Available JavaScript function mcp_neko_memory_search(input): x. Available JavaScript function mcp_neko_memory_save(input): y. Available JavaScript function file_read(input): z.\n" +
      "read `/tmp/runs/stage/workspace/ep/skills/<name>/SKILL.md`, and follow it";
    expect(mcpToolNames(prompt, "neko_memory")).toEqual(["save", "search"]);
    expect(skillsRootFromPrompt(prompt)).toBe("/tmp/runs/stage/workspace/ep/skills");
  });

  it("matches each call to the next tool_end of the same tool", async () => {
    const feed = new RunEventFeed();
    feed.push({ type: "tool_start", id: "ax-skill-a", name: "skill_view" });
    feed.push({ type: "tool_start", id: "ax-op-1", name: "mcp_neko_memory_search" });
    const pending = feed.next("mcp_neko_memory_search", 2_000);
    feed.push({ type: "tool_end", id: "ax-op-1", result: { content: ['{"ok":true}'], is_error: false } });
    expect(await pending).toEqual({ isError: false, content: [{ type: "text", text: '{"ok":true}' }] });
    feed.push({ type: "tool_start", id: "ax-op-2", name: "mcp_neko_memory_search" });
    feed.push({ type: "tool_end", id: "ax-op-2", error: "denied" });
    expect(await feed.next("mcp_neko_memory_search", 100)).toEqual({
      isError: true,
      content: [{ type: "text", text: "denied" }],
    });
    expect((await feed.next("mcp_neko_memory_search", 50)).isError).toBe(true);
  });
});

describe("scripted turn", () => {
  const tools = [{ type: "function" }];
  const distiller = { messages: [{ content: "## Distiller" }, { content: "Question: count the orders" }], tools };
  const executor = { messages: [{ content: "Available JavaScript function mcp_neko_graphjin_execute_graphql(input): q." }], tools };

  it("turns each call into one executor step and answers from the run events", async () => {
    const feed = new RunEventFeed();
    const turn = new ScriptedTurn(async (context) => {
      expect(context.options.prompt).toBe("Question: count the orders");
      expect(await context.listTools("neko_graphjin")).toEqual(["execute_graphql"]);
      const result = await context.call("neko_graphjin", "execute_graphql", { query: "{ a }" });
      return `Rows: ${JSON.stringify(result.content)}`;
    }, feed);
    expect(JSON.parse((await turn.reply(distiller)).content).javascriptCode).toContain("final(");
    const step = JSON.parse((await turn.reply(executor)).content).javascriptCode as string;
    expect(step).toContain('mcp_neko_graphjin_execute_graphql({"query":"{ a }"})');
    feed.push({ type: "tool_start", id: "ax-op-1", name: "mcp_neko_graphjin_execute_graphql" });
    feed.push({ type: "tool_end", id: "ax-op-1", result: { content: ["7"], is_error: false } });
    expect(JSON.parse((await turn.reply(executor)).content).javascriptCode).toContain("final(");
    expect(JSON.parse((await turn.reply(executor)).content).javascriptCode).toContain("final(");
    expect(await turn.reply({ messages: [{ content: "## Answer Synthesis Agent" }] })).toEqual({
      status: 200,
      content: 'Answer: Rows: [{"type":"text","text":"7"}]',
    });
  });

  it("fails the model call when the program throws", async () => {
    const turn = new ScriptedTurn(() => {
      throw new Error("planted program failure");
    }, new RunEventFeed());
    await turn.reply(distiller);
    expect(await turn.reply(executor)).toEqual({ status: 500, content: "planted program failure" });
    expect(await turn.reply(executor)).toEqual({ status: 500, content: "planted program failure" });
    expect(turn.error).toBeInstanceOf(Error);
  });
});

const reachable = await dbReachable();

describe.skipIf(!reachable)("eval install episode", () => {
  const login = evalDatabaseLoginFromEnv();
  const admin = new pg.Pool({ ...login, database: "postgres", ssl: false, max: 1 });
  let host: EvalInstallHost | undefined;
  let model: ScriptedModelServer | undefined;

  afterAll(async () => {
    await host?.close();
    await model?.close();
    await admin.end();
  });

  async function databases(): Promise<string[]> {
    const { rows } = await admin.query<{ datname: string }>(
      `select datname from pg_database where datname like $1`,
      [`${EVAL_EPISODE_DATABASE_PREFIX}%`],
    );
    return rows.map((row) => row.datname);
  }

  it(
    "runs a scripted turn through web and serve, then removes the episode",
    async () => {
      const leftover = `${EVAL_EPISODE_DATABASE_PREFIX}dead_run`;
      await admin.query(`drop database if exists ${leftover}`);
      await admin.query(`create database ${leftover}`);
      const leftoverFolder = join(tmpdir(), `${evalEpisodeFolderPrefix(login)}dead_run`);
      await mkdir(join(leftoverFolder, "home"), { recursive: true });
      const otherServerFolder = join(tmpdir(), `${evalEpisodeFolderPrefix({ ...login, port: login.port + 1 })}live_run`);
      await mkdir(join(otherServerFolder, "runs"), { recursive: true });
      try {
        host = await EvalInstallHost.prepare({ login, lockWaitMs: 600_000 });
        expect(existsSync(join(otherServerFolder, "runs"))).toBe(true);
      } finally {
        await rm(otherServerFolder, { recursive: true, force: true });
      }
      expect(host.binary).toContain(`openneko-eval-bin-${evalServerKey(login)}`);
      await expect(EvalInstallHost.prepare({ login, binary: host.binary, build: false })).rejects.toThrow(
        /another eval run uses this Postgres server/,
      );
      expect(await databases()).not.toContain(leftover);
      expect(existsSync(leftoverFolder)).toBe(false);

      model = await ScriptedModelServer.start();
      const episode = await host.startEpisode(
        { label: "install-test" },
      );
      const { root, database } = episode;
      try {
        expect(await databases()).toContain(database);
        const episodeDb = new pg.Pool({ ...login, database, ssl: false, max: 1 });
        try {
          expect((await episodeDb.query(`select 1 from pg_namespace where nspname = 'pgboss'`)).rowCount).toBe(1);
        } finally {
          await episodeDb.end();
        }
        episode.activate();
        const orgId = "eval-install";
        const userId = "eval-member";
        await createTestOrg(orgId, "Eval install");
        await db().insert(app_user).values({ id: userId, email: "member@eval.invalid", name: "Eval Member", org_id: orgId });
        await seedProvider(orgId, {
          scope: "primary",
          provider: "openai-compatible",
          model: "scripted-deterministic",
          enabled: true,
          config: { url: model.url },
          secrets: { apiKey: maybeEncryptSecret(episode.modelKey) },
        });
        await seedProvider(orgId, { scope: "agent", provider: "ax", enabled: true, config: { backend: "ax" } });
        await rememberWorkMemory({
          orgId,
          userId,
          kind: "business_rule",
          scope: "global",
          pinned: false,
          text: "The quarterly revenue reporting code is REV-EVAL-4242.",
        });
        const workspace = await ensureOrgWorkspace(orgId);
        expect(workspace.skillsRoot.startsWith(episode.agentsRoot)).toBe(true);
        await writeWorkSkill(
          workspace.skillsRoot,
          { name: "eval-brief", description: "Write the eval brief.", body: "Append the code SKI-EVAL-77." },
        );
        await writeWorkSkill(
          workspace.skillsRoot,
          { name: "eval-brief-legacy", description: "Write the retired eval brief.", body: "Retired code SKI-OLD-11." },
        );

        const feed = new RunEventFeed();
        const turn = model.arm(async (context) => {
          const tools = await context.listTools("neko_memory");
          if (!tools.includes("search")) throw new Error(`no memory search tool: ${tools.join(",")}`);
          const found = await context.call("neko_memory", "search", { query: "quarterly revenue reporting code", limit: 5 });
          const text = JSON.stringify(found.content);
          const skill = await context.readSkill("eval-brief");
          return [
            `Memory: ${/REV-EVAL-\d+/.exec(text)?.[0] ?? "none"}`,
            `Skill: ${/SKI-EVAL-\d+/.exec(skill.body)?.[0] ?? "none"}`,
            `Prompt: ${context.options.prompt.includes("What is the reporting code?")}`,
          ].join("\n");
        }, feed);

        const thread = (await (await episode.api(userId, "/api/v1/work/threads", { method: "POST", body: "{}" })).json()) as {
          thread: { id: string };
        };
        const started = await episode.api(userId, `/api/v1/work/threads/${thread.thread.id}/runs`, {
          method: "POST",
          body: JSON.stringify({ message: "What is the reporting code?" }),
        });
        expect(started.status).toBe(200);
        const run = (await started.json()) as { runId: string; actorRole: string };
        expect(run.actorRole).toBe("member");
        const done = await episode.followRun(userId, thread.thread.id, run.runId, (event) => feed.push(event));
        expect(turn.error).toBeUndefined();
        expect(done).toEqual({ type: "done", result: { status: "completed", minutesSaved: 0 } });
        expect(turn.finalText).toBe("Memory: REV-EVAL-4242\nSkill: SKI-EVAL-77\nPrompt: true");
        expect(turn.requests).toEqual(["distiller", "executor", "executor", "executor", "responder"]);

        const storedEvents: Array<Record<string, unknown>> = [];
        const storedDone = await episode.followStoredRun(run.runId, (event) => storedEvents.push(event));
        expect(storedDone).toEqual(done);
        expect(storedEvents).toEqual([...feed.all()]);

        const stored = await episode.api(userId, `/api/v1/work/threads/${thread.thread.id}`);
        const bundle = (await stored.json()) as { messages: Array<{ role: string; content: string }> };
        expect(bundle.messages.filter((m) => m.role === "assistant").map((m) => m.content)).toEqual([turn.finalText]);

        const trace = await episode.readTrace(run.runId);
        expect(trace.events.map((event) => event.operation)).toEqual(
          expect.arrayContaining(["memory.search", "skill.loaded"]),
        );
        expect(trace.steps.some((step) => step.operation === "memory.search")).toBe(true);
        expect((await stat(episode.tracePath)).mode & 0o777).toBe(0o600);
        expect(JSON.stringify(trace)).not.toContain(episode.modelKey);
        expect(await episode.deliveredOutputIds(orgId)).toEqual([]);
      } finally {
        await episode.stop();
      }
      expect(existsSync(root)).toBe(false);
      expect(existsSync(join(root, "serve.sock"))).toBe(false);
      expect(await databases()).not.toContain(database);
    },
    600_000,
  );
});
