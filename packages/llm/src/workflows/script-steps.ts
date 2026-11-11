import { existsSync } from "node:fs";
import { mkdir, writeFile } from "node:fs/promises";
import { join, relative, resolve } from "node:path";
import type { AgentEvent, AgentWorkspace } from "../agent-backend";
import { SandboxRun, sandboxConfig } from "../work/sandbox-service";
import type { WorkflowStep } from "./store";

export type ScriptStepResult = {
  id: string;
  description: string;
  status: "ok" | "failed" | "timeout" | "skipped";
  exitCode: number | null;
  durationMs: number;
  outputPath?: string;
  outputTail?: string;
  error?: string;
};

const DEFAULT_TIMEOUT_SECONDS = 1_200;
const TAIL_BYTES = 4_000;
const MAX_OUTPUT = 1_000_000;

/**
 * Runs a workflow's script steps in order before the agent turn. The first
 * failure skips the remaining steps; the agent receives every result.
 */
export async function runScriptSteps(opts: {
  steps: readonly WorkflowStep[];
  workspace: AgentWorkspace;
  input?: Record<string, unknown>;
  allowedSkills?: readonly string[];
  emit: (event: AgentEvent) => Promise<void>;
  signal?: AbortSignal;
}): Promise<ScriptStepResult[]> {
  const results: ScriptStepResult[] = [];
  const config = sandboxConfig();
  const sandbox = config ? new SandboxRun(config, opts.workspace.orgRoot) : undefined;
  let blocked: string | null = null;
  try {
    for (const step of opts.steps) {
      if (!step.script) continue;
      if (blocked) {
        results.push({
          id: step.id,
          description: step.description,
          status: "skipped",
          exitCode: null,
          durationMs: 0,
          error: `skipped because step ${blocked} did not succeed`,
        });
        continue;
      }
      await opts.emit({ type: "status", message: `Running script step: ${step.description}` });
      const result = await runOne(step, opts, sandbox);
      results.push(result);
      await opts.emit({
        type: "status",
        message: `Script step ${step.id} ${result.status} in ${Math.round(result.durationMs / 1000)}s`,
      });
      if (result.status !== "ok") blocked = step.id;
    }
  } finally {
    await sandbox?.end();
  }
  return results;
}

async function runOne(
  step: WorkflowStep,
  opts: Parameters<typeof runScriptSteps>[0],
  sandbox: SandboxRun | undefined,
): Promise<ScriptStepResult> {
  const script = step.script!;
  const base = { id: step.id, description: step.description };
  const started = Date.now();
  const fail = (error: string): ScriptStepResult => ({
    ...base,
    status: "failed",
    exitCode: null,
    durationMs: Date.now() - started,
    error,
  });

  if (!sandbox) {
    return fail("script steps run in the sandbox service, and this run has none: OPENNEKO_SANDBOX_URL is not set, or an admin turned the Terminal off at Admin, Settings, Agent");
  }
  if (opts.allowedSkills && !opts.allowedSkills.includes(script.skill)) {
    return fail(`skill ${script.skill} is not available to this run`);
  }
  const skillsRoot = resolve(opts.workspace.skillsRoot);
  const skillDir = resolve(skillsRoot, script.skill);
  if (relative(skillsRoot, skillDir).startsWith("..") || !existsSync(skillDir)) {
    return fail(`skill ${script.skill} is not installed`);
  }

  const vars: Record<string, string> = {
    SKILL_DIR: skillDir,
    RUN_DIR: opts.workspace.runRoot,
    ARTIFACT_DIR: opts.workspace.artifactRoot,
  };
  let argv: string[];
  try {
    argv = script.command.map((arg) => expandArg(arg, vars, opts.input ?? {}));
  } catch (err) {
    return fail(err instanceof Error ? err.message : String(err));
  }

  const logDir = join(opts.workspace.runRoot, "steps");
  await mkdir(logDir, { recursive: true });
  const name = safeName(step.id);
  const outputPath = join(logDir, `${name}.output.log`);
  const inputPath = join(logDir, `${name}.input.json`);
  await writeFile(inputPath, JSON.stringify(opts.input ?? {}));
  const timeoutSeconds = script.timeoutSeconds ?? DEFAULT_TIMEOUT_SECONDS;
  const command = `OPENNEKO_WORKFLOW_INPUT="$(cat ${shellQuote(inputPath)})" exec ${argv.map(shellQuote).join(" ")}`;

  let outcome: { code: number | null; timedOut: boolean; output: string; error?: string };
  try {
    const result = await sandbox.exec({
      dir: opts.workspace.runRoot,
      command,
      skillDir,
      env: { RUN_DIR: opts.workspace.runRoot, ARTIFACT_DIR: opts.workspace.artifactRoot },
      timeoutSeconds,
      maxOutput: MAX_OUTPUT,
    }, opts.signal);
    outcome = { code: result.exit_code, timedOut: result.timed_out === true, output: result.output };
  } catch (err) {
    outcome = { code: null, timedOut: false, output: "", error: err instanceof Error ? err.message : String(err) };
  }
  await writeFile(outputPath, outcome.output);
  const tail = Buffer.from(outcome.output);

  return {
    ...base,
    status: outcome.timedOut ? "timeout" : outcome.code === 0 ? "ok" : "failed",
    exitCode: outcome.timedOut ? null : outcome.code,
    durationMs: Date.now() - started,
    outputPath,
    outputTail: tail.subarray(Math.max(0, tail.length - TAIL_BYTES)).toString("utf8").trim(),
    ...(outcome.error
      ? { error: outcome.error }
      : outcome.timedOut
        ? { error: `timed out after ${timeoutSeconds}s` }
        : {}),
  };
}

function shellQuote(value: string): string {
  return `'${value.replace(/'/g, `'\\''`)}'`;
}

const ARG_VAR = /\$\{(input\.[A-Za-z0-9_]+|[A-Z_]+)(?::-([^}]*))?\}/g;

export function expandArg(
  arg: string,
  vars: Record<string, string>,
  input: Record<string, unknown>,
): string {
  return arg.replace(ARG_VAR, (_match, name: string, fallback: string | undefined) => {
    if (name.startsWith("input.")) {
      const value = input[name.slice("input.".length)];
      if (value === undefined || value === null || value === "") {
        if (fallback !== undefined) return fallback;
        throw new Error(`the trigger payload has no ${name.slice(6)} for \${${name}}`);
      }
      return typeof value === "string" ? value : JSON.stringify(value);
    }
    const value = vars[name];
    if (value === undefined) throw new Error(`unknown variable \${${name}}`);
    return value;
  });
}

/** The `<step_results>` block the agent turn receives. */
export function formatScriptStepResults(results: readonly ScriptStepResult[]): string {
  if (results.length === 0) return "";
  const body = results
    .map((r) => {
      const lines = [
        `<step id="${r.id}" status="${r.status}" exit_code="${r.exitCode ?? ""}" duration_s="${Math.round(r.durationMs / 1000)}">`,
        `description: ${r.description}`,
      ];
      if (r.error) lines.push(`error: ${r.error}`);
      if (r.outputPath) lines.push(`output_file: ${r.outputPath}`);
      if (r.outputTail) lines.push(`output_tail:\n${r.outputTail}`);
      lines.push("</step>");
      return lines.join("\n");
    })
    .join("\n");
  return `<step_results>
The workflow's script steps already ran in the sandbox, in the run directory,
before this turn.
Use their output files. Rerun a step's command only to fix a failure you
understand.

${body}
</step_results>`;
}

function safeName(id: string): string {
  return id.replace(/[^A-Za-z0-9_-]/g, "_").slice(0, 60) || "step";
}
