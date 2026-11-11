import { randomBytes } from "node:crypto";
import { request as httpRequest } from "node:http";
import { request as httpsRequest } from "node:https";

/** The bundled skills that run scripts. Without a sandbox no run sees them. */
export const SANDBOX_SKILLS: ReadonlySet<string> = new Set(["document-extraction", "skill-creator"]);

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

/** One run in the sandbox. End it, so the sandbox frees the run's user. */
export class SandboxRun {
  readonly id = `run-${randomBytes(12).toString("hex")}`;

  constructor(private readonly config: SandboxConfig) {}

  async exec(
    input: {
      workspace: string;
      command: string;
      skillDir?: string;
      env?: Record<string, string>;
      timeoutSeconds: number;
      maxOutput: number;
    },
    signal?: AbortSignal,
  ): Promise<SandboxExecResult> {
    let res: { status: number; text: string };
    try {
      res = await call(this.config, "POST", `/v1/runs/${this.id}/exec`, {
        workspace: input.workspace,
        command: input.command,
        ...(input.skillDir ? { skill_dir: input.skillDir } : {}),
        ...(input.env ? { env: input.env } : {}),
        timeout_seconds: input.timeoutSeconds,
        max_output: input.maxOutput,
      }, signal);
    } catch (error) {
      if (signal?.aborted) throw error;
      throw new Error("the sandbox did not answer");
    }
    if (res.status !== 200) throw new Error(`the sandbox refused the command: ${res.text.trim().slice(0, 300)}`);
    return JSON.parse(res.text) as SandboxExecResult;
  }

  async end(): Promise<void> {
    await call(this.config, "DELETE", `/v1/runs/${this.id}`, undefined).catch(() => undefined);
  }
}
