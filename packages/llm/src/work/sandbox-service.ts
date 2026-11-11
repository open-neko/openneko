import { randomBytes } from "node:crypto";
import type { Dirent, Stats } from "node:fs";
import { chmod, lstat, mkdir, readdir, readFile, rm, writeFile } from "node:fs/promises";
import path from "node:path";
import { request as httpRequest } from "node:http";
import { request as httpsRequest } from "node:https";

/** The bundled skills that run scripts. Without a sandbox no run sees them. */
export const SANDBOX_SKILLS: ReadonlySet<string> = new Set(["docx", "pdf", "pptx", "skill-creator", "xlsx"]);

export type SandboxConfig = { url: string; key?: string };

export type SandboxExecResult = {
  exit_code: number;
  output: string;
  timed_out?: boolean;
  truncated?: boolean;
};

export function sandboxConfig(env: NodeJS.ProcessEnv = process.env): SandboxConfig | undefined {
  const url = env.OPENNEKO_SANDBOX_URL?.trim();
  if (!url) return undefined;
  return { url, ...(env.OPENNEKO_SANDBOX_KEY ? { key: env.OPENNEKO_SANDBOX_KEY } : {}) };
}

export function skillRunnable(name: string, sandboxOn: boolean = sandboxConfig() !== undefined): boolean {
  return !SANDBOX_SKILLS.has(name) || sandboxOn;
}

type Target = { socketPath?: string; protocol: "http:" | "https:"; host?: string; port?: string; base: string };

function target(raw: string): Target {
  const url = new URL(raw);
  if (url.protocol === "unix:") {
    if (!url.pathname || url.pathname === "/") throw new Error("OPENNEKO_SANDBOX_URL needs a socket path");
    return { socketPath: url.pathname, protocol: "http:", base: "" };
  }
  if (url.protocol === "http:" || url.protocol === "https:") {
    return { protocol: url.protocol, host: url.hostname, port: url.port, base: url.pathname.replace(/\/+$/, "") };
  }
  throw new Error("OPENNEKO_SANDBOX_URL must start with unix://, http:// or https://");
}

function call(
  config: SandboxConfig,
  method: string,
  path: string,
  body: unknown,
  signal?: AbortSignal,
): Promise<{ status: number; text: string }> {
  const to = target(config.url);
  const payload = body === undefined ? undefined : JSON.stringify(body);
  const send = to.protocol === "https:" ? httpsRequest : httpRequest;
  return new Promise((resolve, reject) => {
    const req = send(
      {
        method,
        path: to.base + path,
        ...(to.socketPath ? { socketPath: to.socketPath } : { host: to.host, ...(to.port ? { port: to.port } : {}) }),
        headers: {
          ...(payload ? { "content-type": "application/json", "content-length": Buffer.byteLength(payload) } : {}),
          ...(config.key ? { authorization: `Bearer ${config.key}` } : {}),
        },
        ...(signal ? { signal } : {}),
      },
      (res) => {
        const chunks: Buffer[] = [];
        res.on("data", (chunk: Buffer) => chunks.push(chunk));
        res.on("end", () => resolve({ status: res.statusCode ?? 0, text: Buffer.concat(chunks).toString("utf8") }));
        res.on("error", reject);
      },
    );
    req.on("error", reject);
    req.end(payload);
  });
}

export async function checkSandbox(config: SandboxConfig, signal?: AbortSignal): Promise<void> {
  let res: { status: number; text: string };
  try {
    res = await call(config, "GET", "/v2/check", undefined, signal);
  } catch (error) {
    if (signal?.aborted) throw error;
    throw new Error("the sandbox did not answer");
  }
  if (res.status === 401) throw new Error("the sandbox refused the key");
  if (res.status === 404) throw new Error("the sandbox is older than this release; rebuild it from recipes/sandbox");
  if (res.status !== 200) throw new Error(`the sandbox answered HTTP ${res.status}`);
}

const MAX_TRANSFER = 128 << 20;

/** The run's job file and runtime state; they never go to the sandbox. */
const PRIVATE_DIR = ".openneko";

type FileData = { path: string; mode: number; data: string };
type FileState = string;
type ExecAnswer = SandboxExecResult & {
  dirs?: string[];
  changed?: FileData[];
  deleted?: string[];
  skipped?: Array<{ path: string; reason: string }>;
};

class LostRun extends Error {}

function stateOf(info: Stats): FileState {
  return `${info.size}:${info.mtimeMs}:${(info.mode & 0o111) !== 0}`;
}

function isPrivate(rel: string): boolean {
  return rel.split("/").includes(PRIVATE_DIR);
}

/**
 * One run in the sandbox. The sandbox keeps its own copy of `root`: each
 * command carries the files that changed here, and its answer carries the
 * files that changed there. End the run, so the sandbox frees its user.
 */
export class SandboxRun {
  readonly id = `run-${randomBytes(12).toString("hex")}`;
  private readonly root: string;
  private started = false;
  private synced = new Map<string, FileState>();
  private dirs = new Set<string>();
  private queue: Promise<unknown> = Promise.resolve();

  constructor(private readonly config: SandboxConfig, root: string) {
    this.root = path.resolve(root);
  }

  exec(
    input: {
      command: string;
      dir?: string;
      skillDir?: string;
      env?: Record<string, string>;
      timeoutSeconds: number;
      maxOutput: number;
    },
    signal?: AbortSignal,
  ): Promise<SandboxExecResult> {
    const next = this.queue.then(() => this.execNow(input, signal));
    this.queue = next.catch(() => undefined);
    return next;
  }

  private relative(target: string, what: string): string {
    const rel = path.relative(this.root, path.resolve(target));
    if (rel.startsWith("..") || path.isAbsolute(rel)) throw new Error(`the sandbox refused the command: ${what} is outside the run`);
    return rel === "" ? "." : rel.split(path.sep).join("/");
  }

  private async execNow(
    input: Parameters<SandboxRun["exec"]>[0],
    signal?: AbortSignal,
  ): Promise<SandboxExecResult> {
    const body: Record<string, unknown> = {
      root: this.root,
      command: input.command,
      timeout_seconds: input.timeoutSeconds,
      max_output: input.maxOutput,
    };
    if (input.dir) body.dir = this.relative(input.dir, "the working folder");
    if (input.skillDir) body.skill_dir = this.relative(input.skillDir, "the skill folder");
    if (input.env) {
      body.env = Object.fromEntries(Object.entries(input.env).map(([name, value]) => [
        name,
        value.split(path.delimiter).map((part) => this.relative(part, name)).join(":"),
      ]));
    }
    let answer: ExecAnswer;
    try {
      answer = await this.send(body, signal);
    } catch (error) {
      if (!(error instanceof LostRun) || !this.started) throw error;
      this.started = false;
      this.synced = new Map();
      this.dirs = new Set();
      answer = await this.send(body, signal);
    }
    this.started = true;
    await this.apply(answer);
    const skipped = answer.skipped ?? [];
    const note = skipped.length
      ? `\n[these files stay in the sandbox: ${skipped.slice(0, 10).map((f) => `${f.path} (${f.reason})`).join(", ")}${skipped.length > 10 ? `, and ${skipped.length - 10} more` : ""}]`
      : "";
    return {
      exit_code: answer.exit_code,
      output: answer.output + note,
      ...(answer.timed_out !== undefined ? { timed_out: answer.timed_out } : {}),
      ...(answer.truncated !== undefined ? { truncated: answer.truncated } : {}),
    };
  }

  private async changes(): Promise<{ dirs: string[]; put: Array<FileData & { state: FileState }>; remove: string[] }> {
    const dirs: string[] = [];
    const put: Array<FileData & { state: FileState }> = [];
    const seen = new Set<string>();
    let total = 0;
    const walk = async (dir: string): Promise<void> => {
      let entries: Dirent[];
      try {
        entries = await readdir(dir, { withFileTypes: true });
      } catch (error) {
        if ((error as NodeJS.ErrnoException).code === "ENOENT" && dir !== this.root) return;
        throw error;
      }
      for (const entry of entries.sort((a, b) => a.name.localeCompare(b.name))) {
        const full = path.join(dir, entry.name);
        if (entry.isDirectory()) {
          if (entry.name === PRIVATE_DIR) continue;
          const rel = this.relative(full, "a folder");
          if (!this.dirs.has(rel)) dirs.push(rel);
          await walk(full);
          continue;
        }
        if (!entry.isFile()) continue;
        const rel = this.relative(full, "a file");
        if (isPrivate(rel)) continue;
        const info = await lstat(full);
        seen.add(rel);
        const state = stateOf(info);
        if (this.synced.get(rel) === state) continue;
        total += info.size;
        if (total > MAX_TRANSFER) {
          throw new Error(`the sandbox refused the command: the run changed by more than ${MAX_TRANSFER >> 20} MiB since the last command`);
        }
        const data = await readFile(full);
        put.push({ path: rel, mode: (info.mode & 0o111) !== 0 ? 0o755 : 0o644, data: data.toString("base64"), state });
      }
    };
    await walk(this.root);
    return { dirs, put, remove: [...this.synced.keys()].filter((rel) => !seen.has(rel)) };
  }

  private async send(body: Record<string, unknown>, signal?: AbortSignal): Promise<ExecAnswer> {
    const { dirs, put, remove } = await this.changes();
    let res: { status: number; text: string };
    try {
      res = await call(this.config, "POST", `/v2/runs/${this.id}/exec`, {
        ...body,
        fresh: !this.started,
        ...(dirs.length ? { dirs } : {}),
        ...(put.length ? { put: put.map(({ state: _state, ...file }) => file) } : {}),
        ...(remove.length ? { delete: remove } : {}),
      }, signal);
    } catch (error) {
      if (signal?.aborted) throw error;
      throw new Error("the sandbox did not answer");
    }
    if (res.status === 409) throw new LostRun("the sandbox lost the run");
    if (res.status === 404) throw new Error("the sandbox is older than this release; rebuild it from recipes/sandbox");
    if (res.status !== 200) throw new Error(`the sandbox refused the command: ${res.text.trim().slice(0, 300)}`);
    for (const dir of dirs) this.dirs.add(dir);
    for (const file of put) this.synced.set(file.path, file.state);
    for (const rel of remove) this.synced.delete(rel);
    return JSON.parse(res.text) as ExecAnswer;
  }

  private target(name: string): string {
    const clean = path.posix.normalize(name);
    if (clean === "." || name.includes("\\") || clean.startsWith("../") || clean === ".." || path.posix.isAbsolute(clean) || isPrivate(clean)) {
      throw new Error(`the sandbox returned a path outside the run: ${JSON.stringify(name)}`);
    }
    return clean;
  }

  /** Writes what the command changed, refusing a path through a link. */
  private async apply(answer: ExecAnswer): Promise<void> {
    for (const name of answer.dirs ?? []) {
      const rel = this.target(name);
      await mkdir(await this.safePath(rel), { recursive: true, mode: 0o770 });
      this.dirs.add(rel);
    }
    for (const file of answer.changed ?? []) {
      const rel = this.target(file.path);
      const full = await this.safePath(rel);
      await mkdir(path.dirname(full), { recursive: true, mode: 0o770 });
      const existing = await lstat(full).catch(() => undefined);
      if (existing && !existing.isFile()) await rm(full, { recursive: true, force: true });
      const mode = (file.mode & 0o111) !== 0 ? 0o770 : 0o660;
      await writeFile(full, Buffer.from(file.data, "base64"), { mode });
      await chmod(full, mode);
      this.synced.set(rel, stateOf(await lstat(full)));
      for (let dir = path.posix.dirname(rel); dir !== "."; dir = path.posix.dirname(dir)) this.dirs.add(dir);
    }
    for (const name of answer.deleted ?? []) {
      const rel = this.target(name);
      await rm(await this.safePath(rel), { force: true });
      this.synced.delete(rel);
    }
  }

  private async safePath(rel: string): Promise<string> {
    let dir = this.root;
    const parts = rel.split("/");
    for (const part of parts.slice(0, -1)) {
      dir = path.join(dir, part);
      const info = await lstat(dir).catch(() => undefined);
      if (info && !info.isDirectory()) throw new Error(`the sandbox returned a path through a link: ${JSON.stringify(rel)}`);
    }
    return path.join(dir, parts[parts.length - 1]!);
  }

  async end(): Promise<void> {
    await call(this.config, "DELETE", `/v2/runs/${this.id}`, undefined).catch(() => undefined);
  }
}
