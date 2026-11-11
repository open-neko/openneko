import { createServer, type Server } from "node:http";
import { mkdir, mkdtemp, readFile, rm, stat, symlink, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { SandboxRun } from "../src/work/sandbox-service";

let root: string;
let server: Server;
let url: string;
const bodies: Array<Record<string, any>> = [];
let replies: Array<{ status: number; body?: unknown }> = [];

beforeEach(async () => {
  root = await mkdtemp(join(tmpdir(), "neko-sandbox-client-"));
  bodies.length = 0;
  replies = [];
  server = createServer((req, res) => {
    let raw = "";
    req.on("data", (chunk) => (raw += chunk));
    req.on("end", () => {
      if (raw) bodies.push(JSON.parse(raw));
      const reply = replies.shift() ?? { status: 200, body: { exit_code: 0, output: "" } };
      res.writeHead(reply.status, { "content-type": "application/json" });
      res.end(JSON.stringify(reply.body ?? {}));
    });
  });
  await new Promise<void>((done) => server.listen(0, "127.0.0.1", done));
  url = `http://127.0.0.1:${(server.address() as { port: number }).port}`;
});

afterEach(async () => {
  await new Promise((done) => server.close(done));
  await rm(root, { recursive: true, force: true });
});

const workspace = () => join(root, "w");
const b64 = (text: string) => Buffer.from(text).toString("base64");
const exec = (run: SandboxRun) => run.exec({ command: "true", timeoutSeconds: 5, maxOutput: 1000 });

describe("SandboxRun", () => {
  it("sends changed files, writes back what the command changed and removes what it deleted", async () => {
    await mkdir(join(workspace(), "runs", "r"), { recursive: true });
    await writeFile(join(workspace(), "a.txt"), "a");
    await writeFile(join(workspace(), "old.txt"), "old");
    replies = [
      {
        status: 200,
        body: {
          exit_code: 0,
          output: "made",
          dirs: ["runs/r/empty"],
          changed: [{ path: "runs/r/artifacts/out.csv", mode: 0o644, data: b64("x,y\n") }],
          deleted: ["old.txt"],
          skipped: [{ path: "link", reason: "not a regular file" }],
        },
      },
    ];
    const run = new SandboxRun({ url }, workspace());
    const result = await exec(run);
    expect(result.output).toBe("made\n[these files stay in the sandbox: link (not a regular file)]");
    expect(await readFile(join(workspace(), "runs", "r", "artifacts", "out.csv"), "utf8")).toBe("x,y\n");
    await expect(stat(join(workspace(), "old.txt"))).rejects.toThrow();
    expect((await stat(join(workspace(), "runs", "r", "empty"))).isDirectory()).toBe(true);
    expect(bodies[0]).toMatchObject({ fresh: true, dirs: ["runs", "runs/r"] });

    await writeFile(join(workspace(), "a.txt"), "edited");
    await rm(join(workspace(), "runs", "r", "artifacts", "out.csv"));
    await exec(run);
    expect(bodies[1]!.dirs).toBeUndefined();
    expect(bodies[1]).toMatchObject({ fresh: false, put: [{ path: "a.txt", data: b64("edited") }], delete: ["runs/r/artifacts/out.csv"] });
  });

  it("sends everything again when the sandbox lost the run", async () => {
    await mkdir(workspace(), { recursive: true });
    await writeFile(join(workspace(), "a.txt"), "a");
    replies = [{ status: 200, body: { exit_code: 0, output: "" } }, { status: 409, body: "lost" }];
    const run = new SandboxRun({ url }, workspace());
    await exec(run);
    await exec(run);
    expect(bodies.map((b) => [b.fresh, (b.put ?? []).length])).toEqual([[true, 1], [false, 0], [true, 1]]);
  });

  it("refuses a returned path outside the run, into its private folder or through a link", async () => {
    const outside = join(root, "outside");
    await mkdir(outside, { recursive: true });
    await mkdir(join(workspace(), "runs", "r", ".openneko"), { recursive: true });
    await writeFile(join(workspace(), "runs", "r", ".openneko", "job.json"), "{}");
    await symlink(outside, join(workspace(), "out"));
    for (const path of ["../escape", "/tmp/escape", "runs/r/.openneko/job.json", "out/escape", "a\\..\\..\\escape"]) {
      replies = [{ status: 200, body: { exit_code: 0, output: "", changed: [{ path, mode: 0o644, data: b64("x") }] } }];
      await expect(exec(new SandboxRun({ url }, workspace()))).rejects.toThrow(/outside the run|through a link/);
    }
    expect(await readFile(join(workspace(), "runs", "r", ".openneko", "job.json"), "utf8")).toBe("{}");
    await expect(stat(join(outside, "escape"))).rejects.toThrow();
    await expect(stat(join(root, "escape"))).rejects.toThrow();
  });

  it("refuses run paths outside the run before it calls the sandbox", async () => {
    await mkdir(workspace(), { recursive: true });
    const run = new SandboxRun({ url }, workspace());
    await expect(run.exec({ command: "true", env: { RUN_DIR: "/etc" }, timeoutSeconds: 5, maxOutput: 1000 })).rejects.toThrow("outside the run");
    await expect(run.exec({ command: "true", skillDir: root, timeoutSeconds: 5, maxOutput: 1000 })).rejects.toThrow("outside the run");
    expect(bodies).toHaveLength(0);
  });
});
