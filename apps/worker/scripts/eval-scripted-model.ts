import { createHash } from "node:crypto";
import { createServer, type IncomingMessage, type Server, type ServerResponse } from "node:http";

export type ScriptedToolResult = {
  isError: boolean;
  content: unknown;
};

export type ScriptedModelContext = {
  options: { prompt: string };
  call(server: string, tool: string, args?: Record<string, unknown>): Promise<ScriptedToolResult>;
  listTools(server: string): Promise<string[]>;
  readSkill(skillName: string): Promise<{ body: string; contentHash: string }>;
};

export type ScriptedModelProgram = (context: ScriptedModelContext) => Promise<string> | string;

export type RunEvent = Record<string, unknown> & { type?: unknown };

const TOOL_RESULT_WAIT_MS = 30_000;

// The harness cuts printed output near 3,000 characters, so tool results
// come from the run's tool_end events and never from the prompt.
export class RunEventFeed {
  private readonly events: RunEvent[] = [];
  private readonly consumed = new Set<string>();
  private readonly waiters = new Set<() => void>();

  push(event: RunEvent): void {
    this.events.push(event);
    for (const wake of this.waiters) wake();
  }

  all(): readonly RunEvent[] {
    return this.events;
  }

  private find(name: string): ScriptedToolResult | undefined {
    for (const start of this.events) {
      const id = typeof start.id === "string" ? start.id : "";
      if (start.type !== "tool_start" || !id.startsWith("ax-op-") || this.consumed.has(id) || start.name !== name) continue;
      const end = this.events.find((event) => event.type === "tool_end" && event.id === id);
      if (!end) return undefined;
      this.consumed.add(id);
      return toolResult(end);
    }
    return undefined;
  }

  async next(name: string, timeoutMs = TOOL_RESULT_WAIT_MS): Promise<ScriptedToolResult> {
    const deadline = Date.now() + timeoutMs;
    for (;;) {
      const found = this.find(name);
      if (found) return found;
      const left = deadline - Date.now();
      if (left <= 0) {
        return { isError: true, content: [{ type: "text", text: `no ${name} result in the run events` }] };
      }
      await new Promise<void>((wake) => {
        const done = () => {
          clearTimeout(timer);
          this.waiters.delete(done);
          wake();
        };
        const timer = setTimeout(done, left);
        this.waiters.add(done);
      });
    }
  }
}

function toolResult(end: RunEvent): ScriptedToolResult {
  if (typeof end.error === "string" && end.error) {
    return { isError: true, content: [{ type: "text", text: end.error }] };
  }
  const result = end.result && typeof end.result === "object" ? (end.result as Record<string, unknown>) : {};
  const raw = Array.isArray(result.content) ? result.content : [result];
  const content = raw.map((entry) =>
    typeof entry === "string"
      ? { type: "text", text: entry }
      : entry && typeof entry === "object" && "text" in entry
        ? entry
        : { type: "text", text: JSON.stringify(entry) },
  );
  return { isError: result.is_error === true || result.isError === true, content };
}

type Action =
  | { kind: "call"; name: string; code: string; resolve(result: ScriptedToolResult): void }
  | { kind: "done"; text: string }
  | { kind: "error"; error: unknown };

type ModelRequest = {
  messages?: Array<{ role?: string; content?: unknown }>;
  tools?: unknown[];
  stream?: boolean;
};

function messageText(content: unknown): string {
  if (typeof content === "string") return content;
  if (Array.isArray(content)) {
    return content
      .map((part) => (part && typeof part === "object" && "text" in part ? String(part.text) : ""))
      .join("");
  }
  return "";
}

export function scriptedRequestKind(request: ModelRequest): "rerank" | "distiller" | "executor" | "responder" {
  const system = messageText(request.messages?.[0]?.content);
  const text = (request.messages ?? []).map((message) => messageText(message.content)).join("\n");
  if (text.includes("Each guide is a written method for one kind of task")) return "rerank";
  if (!request.tools?.length) return "responder";
  if (system.includes("## Distiller")) return "distiller";
  return "executor";
}

function rerankNames(text: string): string[] {
  const guides = /guides?:?\s*\n?([\s\S]*)$/i.exec(text)?.[1] ?? text;
  return [...guides.matchAll(/^- ([A-Za-z0-9._-]+): /gm)].map((match) => match[1]!).slice(0, 2);
}

export function skillsRootFromPrompt(prompt: string): string | undefined {
  return /([^\s`'"]+\/skills)\/<name>\/SKILL\.md/.exec(prompt)?.[1] ?? /([^\s`'"]+\/skills)\/[A-Za-z0-9._-]+\/SKILL\.md/.exec(prompt)?.[1];
}

export function mcpToolNames(prompt: string, server: string): string[] {
  const prefix = `mcp_${server}_`;
  return [
    ...new Set(
      [...prompt.matchAll(/Available JavaScript function ([A-Za-z0-9_]+)\(/g)]
        .map((match) => match[1]!)
        .filter((name) => name.startsWith(prefix))
        .map((name) => name.slice(prefix.length)),
    ),
  ].sort();
}

export const BACKGROUND_RUN_ANSWER = "The scripted model runs no background workflow.";

// A watcher or schedule fire runs its workflow in the episode while a member
// turn is armed. Its requests get a final answer and never reach that turn.
export function backgroundRunReply(request: ModelRequest): { status: number; content: string } | undefined {
  const text = (request.messages ?? []).map((message) => messageText(message.content)).join("\n");
  if (!text.includes("<mode>headless</mode>")) return undefined;
  const kind = scriptedRequestKind(request);
  if (kind === "rerank") return { status: 200, content: `Fitting: ${JSON.stringify(rerankNames(text))}` };
  if (kind === "responder") return { status: 200, content: `Answer: ${BACKGROUND_RUN_ANSWER}` };
  return { status: 200, content: JSON.stringify({ javascriptCode: "final('Report the scripted answer', {})" }) };
}

export class ScriptedTurn {
  prompt = "";
  finalText?: string;
  error?: unknown;
  readonly requests: string[] = [];
  private started = false;
  private executorPrompt = "";
  private awaiting?: Extract<Action, { kind: "call" }>;
  private readonly actions: Action[] = [];
  private actionWaiter?: () => void;

  constructor(
    private readonly program: ScriptedModelProgram,
    readonly feed: RunEventFeed,
  ) {}

  private push(action: Action): void {
    this.actions.push(action);
    this.actionWaiter?.();
  }

  private async nextAction(): Promise<Action> {
    while (this.actions.length === 0) {
      await new Promise<void>((wake) => {
        this.actionWaiter = wake;
      });
      this.actionWaiter = undefined;
    }
    return this.actions.shift()!;
  }

  private call(name: string, args: Record<string, unknown>): Promise<ScriptedToolResult> {
    const code = `try { ${name}(${JSON.stringify(args)}); console.log('called ${name}'); } catch (error) { console.log(String(error)); }`;
    return new Promise((resolve) => this.push({ kind: "call", name, code, resolve }));
  }

  private start(): void {
    this.started = true;
    const context: ScriptedModelContext = {
      options: { prompt: this.prompt },
      call: (server, tool, args = {}) => this.call(`mcp_${server}_${tool}`, args),
      listTools: async (server) => mcpToolNames(this.executorPrompt, server),
      readSkill: async (skillName) => {
        const root = skillsRootFromPrompt(this.executorPrompt) ?? skillsRootFromPrompt(this.prompt);
        if (!root) throw new Error("the prompt names no skills folder");
        const result = await this.call("file_read", { path: `${root}/${skillName}/SKILL.md` });
        const text = result.content
          ? (result.content as Array<{ text?: string }>).map((entry) => entry.text ?? "").join("\n")
          : "";
        if (result.isError) throw new Error(text || `skill ${skillName} could not be read`);
        let body = text;
        try {
          const parsed = JSON.parse(text) as { content?: unknown };
          if (typeof parsed.content === "string") body = parsed.content;
        } catch {}
        return { body, contentHash: createHash("sha256").update(body).digest("hex") };
      },
    };
    void Promise.resolve()
      .then(() => this.program(context))
      .then(
        (text) => this.push({ kind: "done", text }),
        (error) => this.push({ kind: "error", error }),
      );
  }

  async reply(request: ModelRequest): Promise<{ status: number; content: string }> {
    const kind = scriptedRequestKind(request);
    const text = (request.messages ?? []).map((message) => messageText(message.content)).join("\n");
    this.requests.push(kind);
    if (kind === "rerank") {
      return { status: 200, content: `Fitting: ${JSON.stringify(rerankNames(text))}` };
    }
    if (kind === "responder") {
      return { status: 200, content: `Answer: ${this.finalText ?? ""}` };
    }
    if (kind === "distiller") {
      if (!this.prompt) this.prompt = messageText(request.messages?.at(-1)?.content);
      return { status: 200, content: JSON.stringify({ javascriptCode: "final('Run the scripted program', {})" }) };
    }
    this.executorPrompt = text;
    if (!this.prompt) this.prompt = messageText(request.messages?.at(-1)?.content);
    if (this.error !== undefined) {
      return { status: 500, content: this.error instanceof Error ? this.error.message : String(this.error) };
    }
    if (this.finalText !== undefined) {
      return { status: 200, content: JSON.stringify({ javascriptCode: "final('Report the scripted answer', {})" }) };
    }
    if (!this.started) this.start();
    if (this.awaiting) {
      const awaiting = this.awaiting;
      this.awaiting = undefined;
      awaiting.resolve(await this.feed.next(awaiting.name));
    }
    const action = await this.nextAction();
    if (action.kind === "call") {
      this.awaiting = action;
      return { status: 200, content: JSON.stringify({ javascriptCode: action.code }) };
    }
    if (action.kind === "error") {
      this.error = action.error;
      return { status: 500, content: action.error instanceof Error ? action.error.message : String(action.error) };
    }
    this.finalText = action.text;
    return { status: 200, content: JSON.stringify({ javascriptCode: "final('Report the scripted answer', {})" }) };
  }
}

function respond(res: ServerResponse, request: ModelRequest, status: number, content: string): void {
  // The harness drops zero counts, and a promoted result needs totalTokens.
  const usage = { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2 };
  if (status !== 200) {
    res.writeHead(status, { "content-type": "application/json" });
    res.end(JSON.stringify({ error: { message: content } }));
    return;
  }
  if (request.stream) {
    res.writeHead(200, { "content-type": "text/event-stream" });
    const chunk = { choices: [{ index: 0, delta: { role: "assistant", content } }] };
    const end = { choices: [{ index: 0, delta: {}, finish_reason: "stop" }], usage };
    res.end(`data: ${JSON.stringify(chunk)}\n\ndata: ${JSON.stringify(end)}\n\ndata: [DONE]\n\n`);
    return;
  }
  res.writeHead(200, { "content-type": "application/json" });
  res.end(JSON.stringify({ choices: [{ index: 0, message: { role: "assistant", content }, finish_reason: "stop" }], usage }));
}

// 127.0.0.2: the provider code sends 127.0.0.1 and localhost to
// host.docker.internal.
export class ScriptedModelServer {
  url = "";
  private turn?: ScriptedTurn;

  private constructor(private readonly server: Server) {}

  static async start(host = "127.0.0.2"): Promise<ScriptedModelServer> {
    let self: ScriptedModelServer | undefined;
    const server = createServer((req, res) => void self!.handle(req, res));
    self = new ScriptedModelServer(server);
    await new Promise<void>((done, reject) => {
      server.once("error", reject);
      server.listen(0, host, () => done());
    });
    const address = server.address();
    if (!address || typeof address !== "object") throw new Error("the scripted model has no address");
    self.url = `http://${host}:${address.port}/v1`;
    return self;
  }

  arm(program: ScriptedModelProgram, feed: RunEventFeed): ScriptedTurn {
    this.turn = new ScriptedTurn(program, feed);
    return this.turn;
  }

  private async handle(req: IncomingMessage, res: ServerResponse): Promise<void> {
    let body = "";
    for await (const chunk of req) body += chunk;
    let request: ModelRequest;
    try {
      request = JSON.parse(body) as ModelRequest;
    } catch {
      respond(res, {}, 400, "the request is not JSON");
      return;
    }
    const background = backgroundRunReply(request);
    if (background) {
      respond(res, request, background.status, background.content);
      return;
    }
    if (!this.turn) {
      respond(res, request, 503, "no scripted turn is armed");
      return;
    }
    try {
      const { status, content } = await this.turn.reply(request);
      respond(res, request, status, content);
    } catch (cause) {
      respond(res, request, 500, cause instanceof Error ? cause.message : String(cause));
    }
  }

  async close(): Promise<void> {
    this.server.closeAllConnections();
    await new Promise<void>((done) => this.server.close(() => done()));
  }
}
