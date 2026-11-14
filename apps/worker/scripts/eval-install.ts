import { spawn, spawnSync, type ChildProcess } from "node:child_process";
import { createHash, createHmac, randomBytes } from "node:crypto";
import { createWriteStream, existsSync } from "node:fs";
import { chmod, mkdir, mkdtemp, readdir, readFile, rm, writeFile } from "node:fs/promises";
import { createServer, type Server } from "node:http";
import { createConnection, createServer as createNetServer } from "node:net";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import pg from "pg";
import PgBoss from "pg-boss";
import { reconnectPool } from "@neko/db";
import { _resetSecretKeyCacheForTesting } from "@neko/llm/secrets";
import { createActionRequest } from "@neko/llm/workflows";
import { createAdminHandler } from "../src/admin-server";
import { runActionExecute } from "../src/jobs/action-execute";

export const EVAL_TEMPLATE_DATABASE = "openneko_eval_template";
export const EVAL_EPISODE_DATABASE_PREFIX = "openneko_eval_ep_";
export const EVAL_EPISODE_FOLDER_PREFIX = "openneko-eval-ep-";
export const EVAL_MEMORY_RESERVE = 2 * 1024 ** 3;
export const EVAL_EPISODE_MEMORY = 1024 ** 3;
export const EVAL_MEMORY_WAIT_MS = 15 * 60_000;
export const EVAL_MEMORY_POLL_MS = 15_000;

const REPO_ROOT = fileURLToPath(new URL("../../../", import.meta.url));
const START_TIMEOUT_MS = 60_000;
const STOP_GRACE_MS = 10_000;

export type EvalDatabaseLogin = {
  host: string;
  port: number;
  user: string;
  password: string;
  sslmode: string;
};

// Each Postgres server has its own folders: the leftover drop of one eval
// run must not delete the live episodes of a run on another server.
export function evalServerKey(login: Pick<EvalDatabaseLogin, "host" | "port">): string {
  return createHash("sha256").update(`${login.host}:${login.port}`).digest("hex").slice(0, 12);
}

export function evalEpisodeFolderPrefix(login: Pick<EvalDatabaseLogin, "host" | "port">): string {
  return `${EVAL_EPISODE_FOLDER_PREFIX}${evalServerKey(login)}-`;
}

export function evalDatabaseLoginFromEnv(env: NodeJS.ProcessEnv = process.env): EvalDatabaseLogin {
  return {
    host: env.NEKO_PG_HOST || "127.0.0.1",
    port: Number(env.NEKO_PG_PORT || 5432),
    user: env.NEKO_PG_USER || "neko",
    password: env.NEKO_PG_PASSWORD || "secret",
    sslmode: env.NEKO_PG_SSLMODE || "disable",
  };
}

function databaseEnv(login: EvalDatabaseLogin, database: string): Record<string, string> {
  return {
    NEKO_PG_HOST: login.host,
    NEKO_PG_PORT: String(login.port),
    NEKO_PG_USER: login.user,
    NEKO_PG_PASSWORD: login.password,
    NEKO_PG_SSLMODE: login.sslmode,
    NEKO_PG_DATABASE: database,
    OPENNEKO_PG_ENV_OVERRIDE: "1",
  };
}

export function episodeDatabaseName(label: string): string {
  const safe = label.toLowerCase().replace(/[^a-z0-9_]/g, "_").slice(0, 24);
  return `${EVAL_EPISODE_DATABASE_PREFIX}${safe}_${randomBytes(4).toString("hex")}`;
}

function quoteIdent(name: string): string {
  if (!/^[a-z0-9_]+$/.test(name)) throw new Error(`unsafe database name: ${name}`);
  return `"${name}"`;
}

export function evalSessionCookie(secret: string, userId: string, now = Date.now()): string {
  const expires = Math.floor(now / 1000) + 12 * 3600;
  const mac = createHmac("sha256", secret).update(`${userId}.${expires}`).digest("base64url");
  return `openneko_session=${userId}.${expires}.${mac}`;
}

export function availableMemoryBytes(meminfo: string): number | undefined {
  const match = /^MemAvailable:\s+(\d+)\s+kB$/m.exec(meminfo);
  return match ? Number(match[1]) * 1024 : undefined;
}

export function evalMemoryReserveFromEnv(env: NodeJS.ProcessEnv = process.env): number {
  const raw = env.OPENNEKO_EVAL_MEMORY_RESERVE_GB?.trim();
  if (!raw) return EVAL_MEMORY_RESERVE;
  const gb = Number(raw);
  if (!Number.isFinite(gb) || gb < 0) throw new Error(`OPENNEKO_EVAL_MEMORY_RESERVE_GB is not a number of GB: ${raw}`);
  return gb * 1024 ** 3;
}

export function parallelEpisodeCap(available: number | undefined, reserve = EVAL_MEMORY_RESERVE): number {
  if (available === undefined) return 1;
  return Math.max(0, Math.floor((available - reserve) / EVAL_EPISODE_MEMORY));
}

export type EpisodeMemoryWait = {
  readMeminfo: () => Promise<string>;
  reserve: number;
  waitMs?: number;
  pollMs?: number;
  sleep?: (ms: number) => Promise<void>;
  log?: (line: string) => void;
};

export async function waitForEpisodeMemory(wait: EpisodeMemoryWait): Promise<number> {
  const limit = wait.waitMs ?? EVAL_MEMORY_WAIT_MS;
  const poll = wait.pollMs ?? EVAL_MEMORY_POLL_MS;
  const sleep = wait.sleep ?? ((ms: number) => new Promise<void>((wake) => setTimeout(wake, ms)));
  const need = (wait.reserve + EVAL_EPISODE_MEMORY) / 1024 ** 3;
  let waited = 0;
  for (;;) {
    const cap = parallelEpisodeCap(availableMemoryBytes(await wait.readMeminfo()), wait.reserve);
    if (cap >= 1) return cap;
    if (waited >= limit) {
      throw new Error(
        `the eval install needs ${need} GB of free memory to start an episode; it waited ${Math.round(waited / 1000)} s`,
      );
    }
    if (waited === 0) {
      (wait.log ?? ((line: string) => console.error(line)))(
        `eval install: less than ${need} GB of free memory; checking each ${poll / 1000} s for up to ${limit / 60_000} min`,
      );
    }
    const step = Math.min(poll, limit - waited);
    await sleep(step);
    waited += step;
  }
}

async function freePort(host = "127.0.0.1"): Promise<number> {
  return new Promise((resolvePort, reject) => {
    const server = createNetServer();
    server.once("error", reject);
    server.listen(0, host, () => {
      const address = server.address();
      server.close(() => {
        if (address && typeof address === "object") resolvePort(address.port);
        else reject(new Error("no free port"));
      });
    });
  });
}

async function listen(server: Server, host: string): Promise<number> {
  return new Promise((resolvePort, reject) => {
    server.once("error", reject);
    server.listen(0, host, () => {
      const address = server.address();
      if (address && typeof address === "object") resolvePort(address.port);
      else reject(new Error("no listen address"));
    });
  });
}

function tcpOpen(host: string, port: number): Promise<boolean> {
  return new Promise((done) => {
    const socket = createConnection({ host, port });
    socket.once("connect", () => {
      socket.destroy();
      done(true);
    });
    socket.once("error", () => done(false));
  });
}

async function waitFor(check: () => Promise<boolean>, what: string, logs: () => Promise<string>): Promise<void> {
  const deadline = Date.now() + START_TIMEOUT_MS;
  while (Date.now() < deadline) {
    if (await check()) return;
    await new Promise((wake) => setTimeout(wake, 200));
  }
  throw new Error(`${what} did not start within ${START_TIMEOUT_MS / 1000} s\n${await logs()}`);
}

const liveChildren = new Set<ChildProcess>();
let exitHooksInstalled = false;

function installExitHooks(): void {
  if (exitHooksInstalled) return;
  exitHooksInstalled = true;
  const killAll = () => {
    for (const child of liveChildren) child.kill("SIGKILL");
  };
  process.once("exit", killAll);
  for (const signal of ["SIGINT", "SIGTERM"] as const) {
    process.once(signal, () => {
      killAll();
      process.exit(signal === "SIGINT" ? 130 : 143);
    });
  }
}

async function stopChild(child: ChildProcess | undefined): Promise<void> {
  if (!child || child.exitCode !== null || child.signalCode !== null) {
    if (child) liveChildren.delete(child);
    return;
  }
  const exited = new Promise<void>((done) => child.once("exit", () => done()));
  child.kill("SIGTERM");
  const timer = setTimeout(() => child.kill("SIGKILL"), STOP_GRACE_MS);
  await exited;
  clearTimeout(timer);
  liveChildren.delete(child);
}

export type EvalEpisodeOptions = {
  label: string;
  graphjinConfig?: string;
  env?: Record<string, string>;
};

export type EvalInstallHostOptions = {
  login?: EvalDatabaseLogin;
  binary?: string;
  build?: boolean;
  readMeminfo?: () => Promise<string>;
  memoryReserve?: number;
  memoryWaitMs?: number;
  memoryPollMs?: number;
  lockWaitMs?: number;
};

export class EvalEpisode {
  readonly sessionSecret = randomBytes(32).toString("base64url");
  readonly modelKey = `eval-${randomBytes(12).toString("hex")}`;
  webUrl = "";
  serveUrl = "";
  workerUrl = "";
  private web?: ChildProcess;
  private serve?: ChildProcess;
  private worker?: Server;
  private events?: pg.Pool;
  private savedEnv?: Map<string, string | undefined>;
  private stopped = false;

  constructor(
    private readonly host: EvalInstallHost,
    readonly database: string,
    readonly root: string,
  ) {}

  get home(): string {
    return join(this.root, "home");
  }

  get configHome(): string {
    return join(this.home, ".config");
  }

  get agentsRoot(): string {
    return join(this.configHome, "openneko", "agents");
  }

  get runsRoot(): string {
    return join(this.root, "runs");
  }

  get filesRoot(): string {
    return join(this.root, "files");
  }

  get socketPath(): string {
    return join(this.root, "serve.sock");
  }

  get tracePath(): string {
    return join(this.root, "trace.jsonl");
  }

  env(): Record<string, string> {
    return {
      ...databaseEnv(this.host.login, this.database),
      HOME: this.home,
      XDG_CONFIG_HOME: this.configHome,
      OPENNEKO_SERVE_SOCKET: this.socketPath,
      OPENNEKO_SERVE_URL: this.serveUrl,
      OPENNEKO_AGENT_RUNS_DIR: this.runsRoot,
      OPENNEKO_FILES_DIR: this.filesRoot,
      OPENNEKO_FILES_INDEX_DIR: join(this.root, "file-index"),
      OPENNEKO_SESSION_SECRET: this.sessionSecret,
      WORKER_ADMIN_URL: this.workerUrl,
    };
  }

  activate(): void {
    if (this.savedEnv) return;
    const env = this.env();
    this.savedEnv = new Map(Object.keys(env).map((name) => [name, process.env[name]]));
    Object.assign(process.env, env);
    _resetSecretKeyCacheForTesting();
  }

  async deactivate(): Promise<void> {
    if (!this.savedEnv) return;
    for (const [name, value] of this.savedEnv) {
      if (value === undefined) delete process.env[name];
      else process.env[name] = value;
    }
    this.savedEnv = undefined;
    _resetSecretKeyCacheForTesting();
    await reconnectPool();
  }

  sessionCookie(userId: string): string {
    return evalSessionCookie(this.sessionSecret, userId);
  }

  async api(userId: string, path: string, init: RequestInit = {}): Promise<Response> {
    const headers = new Headers(init.headers);
    headers.set("cookie", this.sessionCookie(userId));
    headers.set("origin", this.webUrl);
    if (init.body !== undefined && !headers.has("content-type")) {
      headers.set("content-type", "application/json");
    }
    return fetch(`${this.webUrl}${path}`, { ...init, headers });
  }

  async followRun(
    userId: string,
    threadId: string,
    runId: string,
    onEvent: (event: Record<string, unknown>) => void,
    signal?: AbortSignal,
  ): Promise<Record<string, unknown> | undefined> {
    const res = await this.api(userId, `/api/v1/work/threads/${threadId}/runs/${runId}/events`, {
      headers: { accept: "text/event-stream" },
      ...(signal ? { signal } : {}),
    });
    if (!res.ok || !res.body) throw new Error(`the run stream answered ${res.status}`);
    const decoder = new TextDecoder();
    let buffered = "";
    for await (const chunk of res.body as AsyncIterable<Uint8Array>) {
      buffered += decoder.decode(chunk, { stream: true });
      let end: number;
      while ((end = buffered.indexOf("\n\n")) >= 0) {
        const frame = buffered.slice(0, end);
        buffered = buffered.slice(end + 2);
        const data = frame
          .split("\n")
          .filter((line) => line.startsWith("data: "))
          .map((line) => line.slice(6))
          .join("\n");
        if (!data) continue;
        const event = JSON.parse(data) as Record<string, unknown>;
        if (event.type === "hello") continue;
        onEvent(event);
        if (event.type === "done") return event;
      }
    }
    return undefined;
  }

  // A workflow run's thread has no owner, so no user may open its live
  // stream; its stored events are the same rows in the same order.
  async followStoredRun(
    runId: string,
    onEvent: (event: Record<string, unknown>) => void,
    signal?: AbortSignal,
    timeoutMs = 900_000,
  ): Promise<Record<string, unknown> | undefined> {
    if (!this.events) {
      this.events = new pg.Pool({
        host: this.host.login.host,
        port: this.host.login.port,
        user: this.host.login.user,
        password: this.host.login.password,
        database: this.database,
        ssl: this.host.login.sslmode === "disable" ? false : { rejectUnauthorized: this.host.login.sslmode === "verify-full" },
        max: 1,
      });
      this.events.on("error", () => {});
    }
    const deadline = Date.now() + timeoutMs;
    let after = "0";
    while (Date.now() < deadline) {
      signal?.throwIfAborted();
      const { rows } = await this.events.query<{ id: string; payload: Record<string, unknown> }>(
        `select e.id::text as id, e.payload from work_run_event e where e.run_id::text = $1 and e.id > $2::bigint order by e.id`,
        [runId, after],
      );
      for (const row of rows) {
        after = row.id;
        onEvent(row.payload);
        if (row.payload.type === "done") return row.payload;
      }
      await new Promise((wake) => setTimeout(wake, 100));
    }
    throw new Error(`run ${runId} sent no done event within ${timeoutMs / 1000} s`);
  }

  // Async, because the driver process may serve the source that the job queries.
  async jobsRun(kind: string, orgId: string, at?: Date): Promise<Record<string, unknown>> {
    const args = ["jobs", "run", kind, "--org", orgId, ...(at ? ["--at", at.toISOString()] : [])];
    const child = spawn(this.host.binary, args, {
      env: { ...process.env, ...this.env() },
      stdio: ["ignore", "pipe", "pipe"],
    });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (chunk) => (stdout += chunk));
    child.stderr.on("data", (chunk) => (stderr += chunk));
    const timer = setTimeout(() => child.kill("SIGKILL"), 120_000);
    const status = await new Promise<number | null>((done, reject) => {
      child.once("error", reject);
      child.once("close", (code) => done(code));
    }).finally(() => clearTimeout(timer));
    if (status !== 0) {
      throw new Error(`openneko jobs run ${kind} failed (${status}): ${stderr || stdout}`);
    }
    return JSON.parse(stdout) as Record<string, unknown>;
  }

  // openneko web queues a channel_deliver job per bound channel when a
  // workflow emits an output; the binding is the one bindEvalChannel adds.
  async bindEvalChannel(orgId: string): Promise<void> {
    await this.query(
      `insert into user_group (org_id, slug, name, kind)
         select $1, 'administrators', 'Administrators', 'system'
         where not exists (select 1 from user_group where org_id = $1 and slug = 'administrators')`,
      [orgId],
    );
    await this.query(
      `insert into delivery_binding (org_id, audience, channel_plugin, recipient, enabled)
         values ($1, 'administrators', 'telegram', '{"chatId":"eval"}', true)`,
      [orgId],
    );
  }

  async deliveredOutputIds(orgId: string): Promise<string[]> {
    const rows = await this.query<{ key: string }>(
      `select args->>'key' as key from river.river_job
         where kind = 'channel_deliver' and args->>'orgId' = $1 and args->>'key' like 'output-%-telegram' order by id`,
      [orgId],
    );
    return rows.map((row) => row.key.slice("output-".length, -"-telegram".length));
  }

  private async query<T extends pg.QueryResultRow>(text: string, values: unknown[]): Promise<T[]> {
    const client = new pg.Client({ ...this.host.login, database: this.database, ssl: false });
    await client.connect();
    try {
      return (await client.query<T>(text, values)).rows;
    } finally {
      await client.end();
    }
  }

  async readTrace(runId: string): Promise<{ events: Record<string, unknown>[]; steps: Record<string, unknown>[] }> {
    const text = await readFile(this.tracePath, "utf8").catch(() => "");
    const events: Record<string, unknown>[] = [];
    const steps: Record<string, unknown>[] = [];
    for (const line of text.split("\n")) {
      if (!line.trim()) continue;
      const entry = JSON.parse(line) as { event?: Record<string, unknown>; content?: Record<string, unknown> };
      if (entry.event?.runId !== runId) continue;
      events.push(entry.event);
      if (entry.content) steps.push(entry.content);
    }
    return { events, steps };
  }

  async logs(): Promise<string> {
    const tail = async (name: string) => {
      const text = await readFile(join(this.root, name), "utf8").catch(() => "");
      return `--- ${name}\n${text.split("\n").slice(-30).join("\n")}`;
    };
    return `${await tail("serve.log")}\n${await tail("web.log")}`;
  }

  async start(options: EvalEpisodeOptions): Promise<void> {
    installExitHooks();
    for (const dir of [this.home, this.runsRoot, this.filesRoot, join(this.root, "app")]) {
      await mkdir(dir, { recursive: true, mode: 0o700 });
    }
    await mkdir(join(this.configHome, "openneko"), { recursive: true, mode: 0o700 });
    await writeFile(join(this.configHome, "openneko", "secret-key"), randomBytes(32).toString("base64"), {
      mode: 0o600,
    });
    await writeFile(join(this.root, "app", "index.html"), "<!doctype html><title>OpenNeko eval</title>\n");

    this.worker = createServer(
      createAdminHandler({
        exit: () => {},
        auth: {
          getAuthProvider: () => ({ pluginName: "eval", providerLabel: "Eval install" }),
          authSignInReady: () => true,
          beginAuth: async () => {
            throw new Error("the eval install signs in with a session cookie");
          },
          completeAuth: async () => {
            throw new Error("the eval install signs in with a session cookie");
          },
        },
        actions: { ready: () => true, execute: (input) => runActionExecute(input) },
        actionRequests: {
          create: async (input) => {
            const request = await createActionRequest(input as Parameters<typeof createActionRequest>[0]);
            return { id: request.id, status: request.status };
          },
        },
      }),
    );
    this.workerUrl = `http://127.0.0.1:${await listen(this.worker, "127.0.0.1")}`;
    const servePort = await freePort();
    const webPort = await freePort();
    this.serveUrl = `http://127.0.0.1:${servePort}`;
    this.webUrl = `http://127.0.0.1:${webPort}`;
    const env = { ...process.env, ...this.env(), ...options.env };
    delete env.OPENNEKO_EVAL_TRACE_FILE;

    this.serve = this.spawn("serve.log", [
      "serve",
      "--addr", `127.0.0.1:${servePort}`,
      "--files-dir", this.filesRoot,
      "--index-dir", join(this.root, "file-index"),
      "--agent-socket", this.socketPath,
      "--graphjin-config", options.graphjinConfig ?? "",
      "--graphjin-addr", `127.0.0.1:${await freePort()}`,
      "--subscriptions-addr", "",
    ], env);
    await waitFor(async () => existsSync(this.socketPath) && (await tcpOpen("127.0.0.1", servePort)), "openneko serve", () => this.logs());

    this.web = this.spawn("web.log", [
      "web",
      "--addr", `127.0.0.1:${webPort}`,
      "--dir", join(this.root, "app"),
      "--node-addr", `127.0.0.1:${await freePort()}`,
      "--jobs=true",
      "--worker-admin-url", this.workerUrl,
    ], { ...env, OPENNEKO_EVAL_TRACE_FILE: this.tracePath });
    await waitFor(async () => {
      try {
        return (await fetch(`${this.webUrl}/api/v1/version`)).ok;
      } catch {
        return false;
      }
    }, "openneko web", () => this.logs());
  }

  private spawn(log: string, args: string[], env: NodeJS.ProcessEnv): ChildProcess {
    const child = spawn(this.host.binary, args, { cwd: this.root, env, stdio: ["ignore", "pipe", "pipe"] });
    liveChildren.add(child);
    const out = createWriteStream(join(this.root, log), { mode: 0o600 });
    child.stdout?.pipe(out, { end: false });
    child.stderr?.pipe(out, { end: false });
    child.once("exit", () => {
      liveChildren.delete(child);
      out.end();
    });
    return child;
  }

  async stop(): Promise<void> {
    if (this.stopped) return;
    this.stopped = true;
    await Promise.allSettled([stopChild(this.web), stopChild(this.serve)]);
    if (this.worker) await new Promise<void>((done) => this.worker!.close(() => done()));
    await this.events?.end();
    await this.deactivate();
    await this.host.dropDatabase(this.database);
    await rm(this.root, { recursive: true, force: true });
    this.host.release(this);
  }
}

export class EvalInstallHost {
  readonly login: EvalDatabaseLogin;
  readonly binary: string;
  private readonly live = new Set<EvalEpisode>();
  private readonly waiters: Array<() => void> = [];
  private maintenance?: pg.Pool;
  private lock?: pg.PoolClient;

  private constructor(private readonly options: EvalInstallHostOptions) {
    this.login = options.login ?? evalDatabaseLoginFromEnv();
    this.binary = options.binary ?? join(tmpdir(), `openneko-eval-bin-${evalServerKey(this.login)}`, "openneko");
  }

  static async prepare(options: EvalInstallHostOptions = {}): Promise<EvalInstallHost> {
    const host = new EvalInstallHost(options);
    try {
      await host.takeLock();
      if (options.build ?? !options.binary) host.buildBinary();
      await host.dropLeftovers();
      await host.migrateTemplate();
    } catch (cause) {
      await host.close();
      throw cause;
    }
    return host;
  }

  private pool(): pg.Pool {
    if (this.maintenance) return this.maintenance;
    this.maintenance = new pg.Pool({
      host: this.login.host,
      port: this.login.port,
      user: this.login.user,
      password: this.login.password,
      database: "postgres",
      ssl: this.login.sslmode === "disable" ? false : { rejectUnauthorized: this.login.sslmode === "verify-full" },
      max: 3,
    });
    this.maintenance.on("error", () => {});
    return this.maintenance;
  }

  // The leftover drop and the template migration would break the episodes
  // of a second eval run on the same Postgres server.
  private async takeLock(): Promise<void> {
    const client = await this.pool().connect();
    const deadline = Date.now() + (this.options.lockWaitMs ?? 0);
    for (;;) {
      const { rows } = await client.query<{ locked: boolean }>(
        `select pg_try_advisory_lock(hashtext('openneko.eval.install')) as locked`,
      );
      if (rows[0]?.locked) break;
      if (Date.now() >= deadline) {
        client.release();
        throw new Error("another eval run uses this Postgres server; wait for it to end");
      }
      await new Promise((wake) => setTimeout(wake, 500));
    }
    this.lock = client;
  }

  private buildBinary(): void {
    const result = spawnSync("go", ["build", "-o", this.binary, "./cmd/openneko"], {
      cwd: resolve(REPO_ROOT, "apps/openneko"),
      encoding: "utf8",
      stdio: ["ignore", "pipe", "pipe"],
    });
    if (result.status !== 0) throw new Error(`go build openneko failed: ${result.stderr}`);
  }

  async dropLeftovers(): Promise<string[]> {
    const { rows } = await this.pool().query<{ datname: string }>(
      `select datname from pg_database d
        where datname like $1
          and not exists (select 1 from pg_stat_activity a where a.datname = d.datname)`,
      [`${EVAL_EPISODE_DATABASE_PREFIX}%`],
    );
    for (const row of rows) await this.dropDatabase(row.datname);
    for (const name of await readdir(tmpdir()).catch(() => [] as string[])) {
      if (name.startsWith(evalEpisodeFolderPrefix(this.login))) {
        await rm(join(tmpdir(), name), { recursive: true, force: true });
      }
    }
    return rows.map((row) => row.datname);
  }

  private async migrateTemplate(): Promise<void> {
    const exists = await this.pool().query(`select 1 from pg_database where datname = $1`, [EVAL_TEMPLATE_DATABASE]);
    if (exists.rowCount === 0) await this.pool().query(`create database ${quoteIdent(EVAL_TEMPLATE_DATABASE)}`);
    const configHome = await mkdtemp(join(tmpdir(), "openneko-eval-migrate-"));
    try {
      const result = spawnSync(this.binary, ["migrate"], {
        env: { ...process.env, ...databaseEnv(this.login, EVAL_TEMPLATE_DATABASE), XDG_CONFIG_HOME: configHome },
        encoding: "utf8",
        timeout: 300_000,
      });
      if (result.status !== 0) throw new Error(`openneko migrate on the eval template failed: ${result.stderr || result.stdout}`);
    } finally {
      await rm(configHome, { recursive: true, force: true });
    }
    await this.installBossSchema();
  }

  // The Node worker creates the pgboss schema at start; an episode runs no
  // worker, and the Go job code sends watcher fires to that schema.
  private async installBossSchema(): Promise<void> {
    const boss = new PgBoss({
      host: this.login.host,
      port: this.login.port,
      user: this.login.user,
      password: this.login.password,
      database: EVAL_TEMPLATE_DATABASE,
      ssl: this.login.sslmode === "disable" ? false : { rejectUnauthorized: this.login.sslmode === "verify-full" },
      supervise: false,
      schedule: false,
    });
    boss.on("error", () => {});
    await boss.start();
    await boss.stop({ graceful: false, wait: true });
  }

  async dropDatabase(name: string): Promise<void> {
    await this.pool().query(`drop database if exists ${quoteIdent(name)} with (force)`);
  }

  private async slot(): Promise<void> {
    const readMeminfo = this.options.readMeminfo ?? (() => readFile("/proc/meminfo", "utf8").catch(() => ""));
    const reserve = this.options.memoryReserve ?? evalMemoryReserveFromEnv();
    for (;;) {
      const cap =
        this.live.size === 0
          ? await waitForEpisodeMemory({
              readMeminfo,
              reserve,
              waitMs: this.options.memoryWaitMs,
              pollMs: this.options.memoryPollMs,
            })
          : parallelEpisodeCap(availableMemoryBytes(await readMeminfo()), reserve);
      if (this.live.size < cap) return;
      await new Promise<void>((wake) => this.waiters.push(wake));
    }
  }

  release(episode: EvalEpisode): void {
    if (this.live.delete(episode)) this.waiters.shift()?.();
  }

  async startEpisode(options: EvalEpisodeOptions): Promise<EvalEpisode> {
    await this.slot();
    const database = episodeDatabaseName(options.label);
    const root = await mkdtemp(join(tmpdir(), evalEpisodeFolderPrefix(this.login)));
    await chmod(root, 0o700);
    const episode = new EvalEpisode(this, database, root);
    this.live.add(episode);
    try {
      await this.pool().query(
        `create database ${quoteIdent(database)} template ${quoteIdent(EVAL_TEMPLATE_DATABASE)}`,
      );
      await episode.start(options);
      return episode;
    } catch (cause) {
      await episode.stop().catch(() => {});
      throw cause;
    }
  }

  async close(): Promise<void> {
    await Promise.allSettled([...this.live].map((episode) => episode.stop()));
    this.lock?.release(true);
    this.lock = undefined;
    await this.maintenance?.end();
    this.maintenance = undefined;
  }
}
