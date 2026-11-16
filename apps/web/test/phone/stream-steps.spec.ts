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

const LONG_TABLES = ["sales_order_header_archive", "customer_territory_assignment", "product_inventory_snapshot"];
const QUERIES = {
  orders: '{ sales_orders(where: { order_date: { gte: "2024-01-01" } }) { id total } }',
  broken: '{ sales_orders(where: { region_code: { eq: "Acme Corp" } }) { nope } }',
  long: `{ ${LONG_TABLES.map((t) => `${t}(where: { warehouse_location_code: { eq: "W1" }, fiscal_period_identifier: { eq: 3 } }) { id }`).join(" ")} }`,
};

test.describe("STREAM2b tool steps in plain words at phone width", () => {
  test("each tool call shows what it does as it starts, a failure says so in words, and the line never widens the page", async ({ browser }) => {
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

    const gates = Array.from({ length: 4 }, () => {
      let open!: () => void;
      let reached!: () => void;
      const opened = new Promise<void>((r) => (open = r));
      const atGate = new Promise<void>((r) => (reached = r));
      return { open, opened, reached, atGate };
    });

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
        void (async () => {
          send({ type: "actor.step", data: { text: "Check the sales data" } });
          send({ type: "tool.started", operation_id: 1, name: tool, data: { query: QUERIES.orders } });
          gates[0].reached();
          await gates[0].opened;
          send({ type: "tool.finished", operation_id: 1, name: tool, data: { content: ["{\"data\":{\"sales_orders\":[]}}"] } });
          send({ type: "tool.started", operation_id: 2, name: tool, data: { query: QUERIES.broken } });
          await sleep(50);
          send({
            type: "tool.finished",
            operation_id: 2,
            name: tool,
            error: "mcp_neko_graphjin_execute_graphql_failed",
            data: { content: ["{\"data\":null,\"errors\":[{\"message\":\"column: 'nope' not found\"}]}"] },
          });
          gates[1].reached();
          await gates[1].opened;
          send({ type: "tool.started", operation_id: 3, name: tool, data: { query: QUERIES.long } });
          gates[2].reached();
          await gates[2].opened;
          send({ type: "tool.finished", operation_id: 3, name: tool, data: { content: ["{\"data\":{}}"] } });
          send({ type: "answer.delta", data: { text: "Sales held steady in 2024.", version: 1 } });
          gates[3].reached();
          await gates[3].opened;
          send({
            type: "run.finished",
            result: {
              status: "completed",
              answer: "Sales held steady in 2024.",
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
      "insert into work_thread (id, org_id, title, created_by_user_id) values ($1, $2, 'Tool steps check', $3)",
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
      const context = await browser.newContext({ baseURL: origin, viewport: { width: 400, height: 860 } });
      const page = await context.newPage();
      await page.goto(`/work/${threadId}`, { waitUntil: "networkidle" });

      const composer = page.locator("textarea.work-input");
      await composer.fill("How did sales move in 2024?");
      await composer.press("Enter");
      const transcript = page.locator(".work-transcript");

      await gates[0].atGate;
      await expect(page.locator(".work-tool-row-name")).toHaveText("Reading sales orders, filtered by order date");
      await expect(transcript).not.toContainText("execute_graphql");
      await expect(transcript).not.toContainText("tool call");
      await expect(transcript).not.toContainText("2024-01-01");
      await noSidewaysScroll(page);
      gates[0].open();

      await gates[1].atGate;
      const step = page.locator(".work-tool-group-step");
      await expect(step).toHaveText("That query failed; trying another way");
      await expect(step).toHaveClass(/work-tool-group-step-failed/);
      await expect(page.locator(".work-tool-group-count")).toHaveText("2 steps");
      await expect(transcript).not.toContainText(/\d+ failed/);
      await expect(transcript).not.toContainText("Acme Corp");
      await expect(transcript).not.toContainText("mcp_neko");
      gates[1].open();

      await gates[2].atGate;
      await expect(step).toContainText("Reading sales order header archive");
      const fit = await step.evaluate((el) => {
        const s = getComputedStyle(el);
        return {
          clipped: el.scrollWidth > el.clientWidth,
          lineHeight: parseFloat(s.lineHeight) || parseFloat(s.fontSize) * 1.4,
          height: el.getBoundingClientRect().height,
          right: el.getBoundingClientRect().right,
          overflow: s.textOverflow,
        };
      });
      expect(fit.clipped, "a long step line is cut, not wrapped").toBe(true);
      expect(fit.overflow).toBe("ellipsis");
      expect(fit.height).toBeLessThanOrEqual(fit.lineHeight * 1.5);
      expect(fit.right).toBeLessThanOrEqual(400);
      await noSidewaysScroll(page);

      await page.locator(".work-tool-group-head").click();
      const rows = page.locator(".work-tool-group-body .work-tool-row-name");
      await expect(rows).toHaveCount(3);
      await expect(rows.nth(0)).toHaveText("Reading sales orders, filtered by order date");
      await expect(rows.nth(1)).toHaveText("That query failed; trying another way");
      await expect(rows.nth(2)).toContainText("Reading sales order header archive");
      await noSidewaysScroll(page);
      gates[2].open();

      await gates[3].atGate;
      gates[3].open();
      await expect(page.locator(".work-status-row")).toHaveCount(0);
      const trail = page.locator(".work-trail");
      await expect(trail).toContainText("3 steps");
      await expect(trail).not.toContainText("tool call");
      await noSidewaysScroll(page);
      await context.close();
    } catch (err) {
      console.error(logs.slice(-4000));
      throw err;
    } finally {
      for (const g of gates) g.open();
      srv.kill("SIGTERM");
      serve.close();
      await client.query("delete from work_thread where id = $1", [threadId]).catch(() => {});
      await client.end();
      rmSync(work, { recursive: true, force: true });
    }
  });
});
