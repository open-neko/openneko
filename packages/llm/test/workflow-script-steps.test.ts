import { spawn } from "node:child_process";
import { mkdir, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { createServer, type Server } from "node:http";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { AgentEvent, AgentWorkspace } from "../src/agent-backend";
import {
  expandArg,
  formatScriptStepResults,
  runScriptSteps,
} from "../src/workflows/script-steps";

let root: string;
let workspace: AgentWorkspace;
let sandbox: Server;
const sandboxCalls: Array<{ method?: string; url?: string; auth?: string; body?: Record<string, unknown> }> = [];

/** Runs commands as the sandbox service does: /bin/sh in the workspace, with only the paths it was given. */
function fakeSandbox(socket: string): Promise<Server> {
  const server = createServer((req, res) => {
    let raw = "";
    req.on("data", (chunk) => (raw += chunk));
    req.on("end", () => {
      const body = raw ? (JSON.parse(raw) as Record<string, any>) : undefined;
      sandboxCalls.push({ method: req.method, url: req.url, auth: req.headers.authorization, body });
      if (req.method === "DELETE") return res.writeHead(204).end();
      const child = spawn("/bin/sh", ["-c", body!.command], {
        cwd: body!.workspace,
        env: { PATH: process.env.PATH, ...body!.env, ...(body!.skill_dir ? { SKILL_DIR: body!.skill_dir } : {}) },
        detached: true,
      });
      let output = "";
      let timedOut = false;
      child.stdout.on("data", (chunk) => (output += chunk));
      child.stderr.on("data", (chunk) => (output += chunk));
      const timer = setTimeout(() => {
        timedOut = true;
        process.kill(-child.pid!, "SIGKILL");
      }, body!.timeout_seconds * 1000);
      child.on("close", (code) => {
        clearTimeout(timer);
        res.writeHead(200, { "content-type": "application/json" });
        res.end(JSON.stringify({ exit_code: timedOut ? -1 : code, output, timed_out: timedOut }));
      });
    });
  });
  return new Promise((done) => server.listen(socket, () => done(server)));
}
const events: AgentEvent[] = [];
const emit = async (event: AgentEvent) => {
  events.push(event);
};

beforeEach(async () => {
  root = await mkdtemp(join(tmpdir(), "neko-steps-"));
  workspace = {
    orgRoot: root,
    skillsRoot: join(root, "skills"),
    memoryRoot: join(root, "memory"),
    knowledgeRoot: join(root, "knowledge"),
    uploadsRoot: join(root, "uploads"),
    runsRoot: join(root, "runs"),
    threadUploadsRoot: join(root, "uploads", "t"),
    runRoot: join(root, "runs", "r"),
    artifactRoot: join(root, "runs", "r", "artifacts"),
    binRoot: join(root, "runs", "r", "bin"),
  };
  await mkdir(join(workspace.skillsRoot, "lead-union", "scripts"), { recursive: true });
  await mkdir(workspace.runRoot, { recursive: true });
  await writeFile(
    join(workspace.skillsRoot, "lead-union", "scripts", "run.py"),
    [
      "import json, os, sys",
      "day = sys.argv[1]",
      "if day == 'fail':",
      "    print('bad day', file=sys.stderr)",
      "    sys.exit(3)",
      "if day == 'sleep':",
      "    import time; time.sleep(30)",
      "open(os.path.join(os.environ['RUN_DIR'], 'out.csv'), 'w').write('day\\n' + day)",
      "print(json.dumps({'day': day, 'input': json.loads(os.environ['OPENNEKO_WORKFLOW_INPUT'])}))",
    ].join("\n"),
  );
  events.length = 0;
  sandboxCalls.length = 0;
  sandbox = await fakeSandbox(join(root, "sandbox.sock"));
  vi.stubEnv("OPENNEKO_SANDBOX_URL", `unix://${join(root, "sandbox.sock")}`);
  vi.stubEnv("OPENNEKO_SANDBOX_KEY", "sandbox-key");
  vi.stubEnv("OPENNEKO_BROKER_TOKEN", "broker-secret");
});

afterEach(async () => {
  vi.unstubAllEnvs();
  await new Promise((done) => sandbox.close(done));
  await rm(root, { recursive: true, force: true });
});

const step = (id: string, day: string, timeoutSeconds?: number) => ({
  id,
  description: `run ${id}`,
  script: {
    skill: "lead-union",
    command: ["python3", "${SKILL_DIR}/scripts/run.py", day],
    ...(timeoutSeconds ? { timeoutSeconds } : {}),
  },
});

describe("expandArg", () => {
  it("expands run variables and trigger input with defaults", () => {
    const vars = { RUN_DIR: "/run" };
    expect(expandArg("${RUN_DIR}/out.csv", vars, {})).toBe("/run/out.csv");
    expect(expandArg("${input.date}", vars, { date: "2026-09-29" })).toBe("2026-09-29");
    expect(expandArg("${input.date:-}", vars, {})).toBe("");
    expect(expandArg("--n=${input.n:-5}", vars, {})).toBe("--n=5");
    expect(() => expandArg("${input.date}", vars, {})).toThrow(/no date/);
    expect(() => expandArg("${HOME}", vars, {})).toThrow(/unknown variable/);
  });
});

describe("runScriptSteps", () => {
  it("runs a skill script in the sandbox from the run directory with the trigger input", async () => {
    const results = await runScriptSteps({
      steps: [
        { id: "plan", description: "agent step" },
        { ...step("union", "${input.date}") },
      ],
      workspace,
      input: { date: "2026-09-29" },
      emit,
    });

    expect(results).toHaveLength(1);
    expect(results[0]).toMatchObject({ id: "union", status: "ok", exitCode: 0 });
    expect(JSON.parse(results[0]!.outputTail!)).toEqual({
      day: "2026-09-29",
      input: { date: "2026-09-29" },
    });
    expect(await readFile(join(workspace.runRoot, "out.csv"), "utf8")).toBe("day\n2026-09-29");
    expect(await readFile(results[0]!.outputPath!, "utf8")).toContain("2026-09-29");
    expect(events.map((e) => e.type)).toEqual(["status", "status"]);

    const [exec, end] = sandboxCalls;
    expect(exec).toMatchObject({ method: "POST", auth: "Bearer sandbox-key" });
    expect(exec!.body).toMatchObject({
      workspace: workspace.runRoot,
      skill_dir: join(workspace.skillsRoot, "lead-union"),
      env: { RUN_DIR: workspace.runRoot, ARTIFACT_DIR: workspace.artifactRoot },
    });
    expect(JSON.stringify(exec!.body)).not.toContain("broker-secret");
    expect(end).toMatchObject({ method: "DELETE", url: exec!.url!.replace(/\/exec$/, "") });
  });

  it("refuses script steps when the install has no sandbox", async () => {
    vi.stubEnv("OPENNEKO_SANDBOX_URL", "");
    const results = await runScriptSteps({ steps: [step("union", "ok")], workspace, emit });
    expect(results[0]).toMatchObject({ status: "failed" });
    expect(results[0]!.error).toContain("OPENNEKO_SANDBOX_URL");
    expect(sandboxCalls).toHaveLength(0);
  });

  it("reports a sandbox that does not answer", async () => {
    vi.stubEnv("OPENNEKO_SANDBOX_URL", `unix://${join(root, "missing.sock")}`);
    const results = await runScriptSteps({ steps: [step("union", "ok")], workspace, emit });
    expect(results[0]).toMatchObject({ status: "failed", error: "the sandbox did not answer" });
  });

  it("skips the remaining steps after a failure", async () => {
    const results = await runScriptSteps({
      steps: [step("first", "fail"), step("second", "ok")],
      workspace,
      emit,
    });

    expect(results.map((r) => r.status)).toEqual(["failed", "skipped"]);
    expect(results[0]).toMatchObject({ exitCode: 3, outputTail: "bad day" });
    expect(results[1]!.error).toContain("step first");
  });

  it("kills a step that exceeds its timeout", async () => {
    const results = await runScriptSteps({
      steps: [step("slow", "sleep", 1)],
      workspace,
      emit,
    });
    expect(results[0]).toMatchObject({ status: "timeout" });
    expect(results[0]!.error).toContain("timed out after 1s");
  });

  it("refuses a skill the run does not hold", async () => {
    const results = await runScriptSteps({
      steps: [step("union", "ok")],
      workspace,
      allowedSkills: ["other-skill"],
      emit,
    });
    expect(results[0]).toMatchObject({ status: "failed" });
    expect(results[0]!.error).toContain("not available");
  });

  it("formats results for the agent turn", async () => {
    const results = await runScriptSteps({
      steps: [step("first", "fail")],
      workspace,
      emit,
    });
    const block = formatScriptStepResults(results);
    expect(block).toContain('<step id="first" status="failed" exit_code="3"');
    expect(block).toContain("output_tail:\nbad day");
    expect(formatScriptStepResults([])).toBe("");
  });
});
