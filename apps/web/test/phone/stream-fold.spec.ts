import { execFileSync, spawn } from "node:child_process";
import { randomUUID } from "node:crypto";
import { cpSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync } from "node:fs";
import net from "node:net";
import { tmpdir } from "node:os";
import path from "node:path";
import { expect, test } from "@playwright/test";
import pg from "pg";

function resolveDatabaseUrl(): string {
  const file = process.env.OPENNEKO_DATABASE_URL_FILE;
  if (file) return readFileSync(file, "utf8").trim();
  if (process.env.DATABASE_URL) return process.env.DATABASE_URL.trim();
  throw new Error("set OPENNEKO_DATABASE_URL_FILE or DATABASE_URL (scripts/phone-local.sh)");
}

function freePort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const s = net.createServer();
    s.on("error", reject);
    s.listen(0, "127.0.0.1", () => {
      const addr = s.address();
      if (!addr || typeof addr === "string") return reject(new Error("no tcp address"));
      s.close(() => resolve(addr.port));
    });
  });
}

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

test.describe("STREAM2e a long run folds its earlier notes and steps at phone width", () => {
  test("the earlier notes and steps fold into one row, the live line stays above the composer, and a reader who scrolls up stays put", async ({ browser }) => {
    test.setTimeout(120_000);
    const dbUrl = resolveDatabaseUrl();
    const client = new pg.Client({ connectionString: dbUrl });
    await client.connect();

    const webRoot = path.resolve(__dirname, "../..");
    const work = mkdtempSync(path.join(tmpdir(), "neko-steps-"));
    const distDir = path.join(work, "dist");
    cpSync(path.join(webRoot, "dist"), distDir, { recursive: true });
    const binPath =
      process.env.OPENNEKO_PHONE_BIN && existsSync(process.env.OPENNEKO_PHONE_BIN)
        ? process.env.OPENNEKO_PHONE_BIN
        : path.join(work, "openneko");
    if (!existsSync(binPath)) {
      execFileSync("go", ["build", "-o", binPath, "./cmd/openneko"], {
        cwd: path.resolve(webRoot, "../openneko"),
        stdio: "inherit",
      });
    }

    const gate = () => {
      let open!: () => void;
      let reached!: () => void;
      const opened = new Promise<void>((r) => (open = r));
      const atGate = new Promise<void>((r) => (reached = r));
      return { open, opened, reached, atGate };
    };
    const longRun = gate();
    const afterScroll = gate();
    const finish = gate();

    const socketPath = path.join(work, "serve.sock");
    const serve = net.createServer((conn) => {
      let buf = "";
      conn.on("data", (chunk: Buffer) => {
        buf += chunk.toString("utf8");
        const nl = buf.indexOf("\n");
        if (nl < 0) return;
        const start = JSON.parse(buf.slice(0, nl)) as { type?: string; spec?: { run_id?: string } };
        buf = "";
        const runId = start.spec?.run_id;
        if (start.type !== "start" || !runId) {
          conn.end();
          return;
        }
        let seq = 0;
        const send = (event: Record<string, unknown>) => {
          seq++;
          if (!conn.destroyed) conn.write(`${JSON.stringify({ type: "event", event: { version: 1, run_id: runId, sequence: seq, ...event } })}\n`);
        };
        const tool = "mcp_neko_graphjin_execute_graphql";
        let op = 0;
        const round = async (n: number) => {
          send({ type: "executor.thought.delta", data: { version: 1, index: n, text: `Checking region ${n}` } });
          await sleep(30);
          op++;
          send({ type: "tool.started", operation_id: op, name: tool, data: { query: `{ sales_orders(where: { territory_id: { eq: ${n} } }) { id total } }` } });
          await sleep(30);
          send({ type: "tool.finished", operation_id: op, name: tool, data: { content: ["{\"data\":{\"sales_orders\":[]}}"] } });
          await sleep(30);
        };
        void (async () => {
          send({ type: "actor.step", data: { text: "Check the sales data" } });
          for (let n = 1; n <= 7; n++) await round(n);
          longRun.reached();
          await longRun.opened;
          await round(8);
          afterScroll.reached();
          finish.reached();
          await finish.opened;
          send({ type: "answer.delta", data: { text: "Sales held steady in every region.", version: 1 } });
          send({
            type: "run.finished",
            result: {
              status: "completed",
              answer: "Sales held steady in every region.",
              usage: { coverage: "complete", requests: 1, reported: 1, input_tokens: 10, output_tokens: 10, total_tokens: 20 },
            },
          });
          if (!conn.destroyed) conn.write(`${JSON.stringify({ type: "exit", code: 0 })}\n`);
          conn.end();
        })();
      });
    });
    await new Promise<void>((resolve, reject) => {
      serve.on("error", reject);
      serve.listen(socketPath, () => resolve());
    });

    const { rows } = await client.query<{ id: string; solo_admin_user_id: string | null }>(
      "select id, solo_admin_user_id from organization limit 1",
    );
    const orgId = rows[0]!.id;
    const userId =
      rows[0]!.solo_admin_user_id ??
      (await client.query<{ id: string }>("select id from app_user where org_id = $1 order by created_at limit 1", [orgId]))
        .rows[0]!.id;
    await client.query(
      `insert into llm_provider_config (org_id, scope, provider, model, enabled, config, secrets)
       values ($1, 'primary', 'openai-compatible', 'scripted-v1', true, '{"url":"http://127.0.0.1:9/v1"}'::jsonb, '{"apiKey":"scripted-key"}'::jsonb),
              ($1, 'research', 'none', null, false, '{}'::jsonb, '{}'::jsonb)
       on conflict (org_id, scope) do update set provider = excluded.provider, model = excluded.model,
         enabled = excluded.enabled, config = excluded.config, secrets = excluded.secrets`,
      [orgId],
    );
    const threadId = randomUUID();
    await client.query(
      "insert into work_thread (id, org_id, title, created_by_user_id) values ($1, $2, 'Fold check', $3)",
      [threadId, orgId, userId],
    );

    const configHome = path.join(work, "config");
    mkdirSync(configHome, { recursive: true });
    const port = await freePort();
    const origin = `http://127.0.0.1:${port}`;
    let logs = "";
    const srv = spawn(binPath, ["web", "--addr", `127.0.0.1:${port}`, "--dir", distDir, "--jobs=false"], {
      env: {
        ...process.env,
        XDG_CONFIG_HOME: configHome,
        XDG_DATA_HOME: path.join(work, "data"),
        OPENNEKO_SERVE_SOCKET: socketPath,
      },
      stdio: ["ignore", "pipe", "pipe"],
    });
    srv.stdout?.on("data", (d: Buffer) => (logs += d.toString("utf8")));
    srv.stderr?.on("data", (d: Buffer) => (logs += d.toString("utf8")));

    const noSidewaysScroll = async (page: import("@playwright/test").Page) =>
      expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(400);

    try {
      await expect
        .poll(async () => (await fetch(`${origin}/api/v1/version`).catch(() => null))?.ok ?? false, { timeout: 20_000 })
        .toBe(true);
      const context = await browser.newContext({ baseURL: origin, viewport: { width: 400, height: 760 } });
      const page = await context.newPage();
      await page.goto(`/work/${threadId}`, { waitUntil: "networkidle" });

      const composer = page.locator("textarea.work-input");
      await composer.fill("How did sales move by region?");
      await composer.press("Enter");
      const transcript = page.locator(".work-transcript");

      await longRun.atGate;
      const fold = page.locator(".work-live-trail");
      await expect(fold).toHaveCount(1);
      await expect(fold).toContainText("Earlier steps");
      await expect(fold).toContainText("6 notes, 6 steps");
      await expect(fold).not.toHaveAttribute("open", "");
      await expect(transcript.locator(".work-progress-summary .work-markdown")).toHaveText("Checking region 7");
      await expect(transcript.locator(".work-tool-group:not(.work-live-trail .work-tool-group)")).toHaveCount(1);

      const live = page.locator(".work-status-row");
      await expect(live).toBeVisible();
      await expect
        .poll(async () => {
          const liveBox = await live.boundingBox();
          const shellBox = await page.locator(".work-composer-shell").boundingBox();
          return liveBox && shellBox ? shellBox.y - (liveBox.y + liveBox.height) : -1;
        }, { message: "the live line sits above the composer" })
        .toBeGreaterThanOrEqual(0);
      await noSidewaysScroll(page);

      await page.locator(".work-live-trail > summary").click();
      await expect(fold).toHaveAttribute("open", "");
      await expect(fold.locator(".work-progress-disclosure")).toHaveCount(6);
      await expect(fold).toContainText("Checking region 1");
      await expect(fold).not.toContainText("execute_graphql");
      await noSidewaysScroll(page);
      await page.locator(".work-live-trail > summary").click();
      await expect(fold).not.toHaveAttribute("open", "");

      await page.mouse.move(200, 300);
      await page.mouse.wheel(0, -2000);
      await expect.poll(() => page.evaluate(() => window.scrollY)).toBe(0);
      longRun.open();
      await afterScroll.atGate;
      await expect(transcript.locator(".work-progress-summary .work-markdown")).toHaveText("Checking region 8");
      await expect(fold).toContainText("7 notes, 7 steps");
      expect(await page.evaluate(() => window.scrollY), "a reader who scrolled up stays put").toBe(0);

      finish.open();
      await expect(page.locator(".work-status-row")).toHaveCount(0);
      await expect(page.locator(".work-live-trail")).toHaveCount(0);
      const trail = page.locator(".work-trail");
      await expect(trail).toHaveCount(1);
      await expect(trail).toContainText("8 notes, 8 steps");
      await noSidewaysScroll(page);
      await context.close();
    } catch (err) {
      console.error(logs.slice(-4000));
      throw err;
    } finally {
      longRun.open();
      finish.open();
      srv.kill("SIGTERM");
      serve.close();
      await client.query("delete from work_thread where id = $1", [threadId]).catch(() => {});
      await client.end();
      rmSync(work, { recursive: true, force: true });
    }
  });
});
