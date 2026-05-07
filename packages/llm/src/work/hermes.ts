import { spawn } from "node:child_process";
import { listSkillNames } from "./workspace";
import { buildWorkPrompt } from "./prompt";
import type { WorkAgentBackend, WorkRunInput, WorkRunResult, WorkSurfaceMessage } from "./types";

export class HermesWorkBackend implements WorkAgentBackend {
  readonly id = "hermes" as const;

  async run(input: WorkRunInput): Promise<WorkRunResult> {
    const prompt = buildWorkPrompt({
      backend: this.id,
      workspace: input.workspace,
      messages: input.messages,
      currentUserMessage: input.currentUserMessage,
      supportsCardTool: false,
      supportsSkillTool: false,
    });
    const skills = await listSkillNames(input.workspace.skillsRoot);
    await input.onEvent({ type: "status", message: "Hermes is working…" });

    return new Promise<WorkRunResult>((resolve) => {
      const args = ["-z", prompt];
      if (skills.length > 0) {
        args.push("--skills", skills.join(","));
      }
      const child = spawn("hermes", args, {
        cwd: input.workspace.orgRoot,
        env: {
          ...process.env,
          HERMES_HOME: input.workspace.hermesHome,
          PATH: `${input.workspace.binRoot}:${process.env.PATH || ""}`,
        },
        stdio: ["ignore", "pipe", "pipe"],
      });

      const stdout: Buffer[] = [];
      const stderr: Buffer[] = [];
      const onAbort = () => {
        child.kill("SIGTERM");
      };
      input.signal.addEventListener("abort", onAbort, { once: true });

      child.stdout.on("data", (chunk: Buffer) => stdout.push(chunk));
      child.stderr.on("data", (chunk: Buffer) => {
        stderr.push(chunk);
        if (input.debug) process.stderr.write(`[hermes-work ${input.runId}] ${chunk}`);
      });
      child.on("error", async (error) => {
        const message = error instanceof Error ? error.message : String(error);
        await input.onEvent({ type: "error", message });
        resolve({
          backend: this.id,
          status: input.signal.aborted ? "cancelled" : "failed",
          finalText: "",
          error: message,
          backendState: input.backendState,
        });
      });
      child.on("close", async (code) => {
        input.signal.removeEventListener("abort", onAbort);
        const text = Buffer.concat(stdout).toString("utf8").trim();
        const err = Buffer.concat(stderr).toString("utf8").trim();
        const parsed = extractHermesCards(text);
        if (parsed.messages.length > 0) {
          await input.onEvent({ type: "surface", messages: parsed.messages });
        }
        const assistantText = parsed.text.trim();
        if (assistantText) {
          await input.onEvent({ type: "message", role: "assistant", content: assistantText });
        }
        if (code === 0) {
          resolve({
            backend: this.id,
            status: input.signal.aborted ? "cancelled" : "completed",
            finalText: assistantText,
            backendState: input.backendState,
          });
          return;
        }
        const message = err || `Hermes exited ${code ?? "unknown"}`;
        await input.onEvent({ type: "error", message });
        resolve({
          backend: this.id,
          status: input.signal.aborted ? "cancelled" : "failed",
          finalText: assistantText,
          error: message,
          backendState: input.backendState,
        });
      });
    });
  }
}

function extractHermesCards(text: string): {
  text: string;
  messages: WorkSurfaceMessage[];
} {
  const match = text.match(/```neko_a2ui\s*([\s\S]*?)```/i);
  if (!match) return { text, messages: [] };
  try {
    const messages = JSON.parse(match[1].trim()) as WorkSurfaceMessage[];
    return {
      text: text.replace(match[0], "").trim(),
      messages: Array.isArray(messages) ? messages : [],
    };
  } catch {
    return { text, messages: [] };
  }
}
