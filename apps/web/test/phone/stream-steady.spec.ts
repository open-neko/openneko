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

const PROSE = Array.from({ length: 12 }, (_, i) => `Week ${i + 1} sales held steady at the usual level. `).join("");
const TABLE = ["| Region | Sales |", "| --- | ---: |", "| East | 12,400 |", "| West | 9,100 |"];
const AFTER = "East led the quarter.";
const CARD_OPEN = '```neko_cards\n{"cards":[{"title":"Hidden payload"}';

test.describe("STREAM1c steady answer stream at phone width", () => {
  test("text grows while the model streams, and a table shows only when complete", async ({ browser }) => {
    test.setTimeout(120_000);
    const dbUrl = resolveDatabaseUrl();
    const client = new pg.Client({ connectionString: dbUrl });
    await client.connect();

    const webRoot = path.resolve(__dirname, "../..");
    const work = mkdtempSync(path.join(tmpdir(), "neko-stream-"));
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

    let proseEndedAt = 0;
    let releaseTable!: () => void;
    const tableReleased = new Promise<void>((r) => (releaseTable = r));
    let tablePartSent!: () => void;
    const tablePart = new Promise<void>((r) => (tablePartSent = r));
    let releaseCard!: () => void;
    const cardReleased = new Promise<void>((r) => (releaseCard = r));
    let cardPartSent!: () => void;
    const cardPart = new Promise<void>((r) => (cardPartSent = r));

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
        const delta = (text: string) => send({ type: "answer.delta", data: { text, version: 1 } });
        void (async () => {
          for (const word of PROSE.split(/(?<= )/)) {
            delta(word);
            await sleep(20);
          }
          proseEndedAt = Date.now();
          delta("\n\n");
          for (const line of TABLE.slice(0, 3)) {
            delta(`${line}\n`);
            await sleep(20);
          }
          tablePartSent();
          await tableReleased;
          delta(`${TABLE[3]}\n\n`);
          await sleep(20);
          delta(AFTER);
          delta(`\n\n${CARD_OPEN}`);
          cardPartSent();
          await cardReleased;
          const answer = `${PROSE}\n\n${TABLE.join("\n")}\n\n${AFTER}\n\n${CARD_OPEN}]}\n\`\`\``;
          send({
            type: "run.finished",
            result: {
              status: "completed",
              answer,
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
      "insert into work_thread (id, org_id, title, created_by_user_id) values ($1, $2, 'Steady stream check', $3)",
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

    try {
      await expect
        .poll(async () => (await fetch(`${origin}/api/v1/version`).catch(() => null))?.ok ?? false, { timeout: 20_000 })
        .toBe(true);
      const context = await browser.newContext({ baseURL: origin, viewport: { width: 400, height: 860 } });
      const page = await context.newPage();
      await page.goto(`/work/${threadId}`, { waitUntil: "networkidle" });
      await page.evaluate(() => {
        const w = window as unknown as { textSamples: Array<{ at: number; text: string }> };
        w.textSamples = [];
        const read = () => {
          const bubbles = document.querySelectorAll(".work-timeline .work-bubble:not(.is-user) .work-markdown");
          const text = [...bubbles].map((b) => b.textContent ?? "").join("\n");
          const last = w.textSamples.at(-1);
          if (!last || last.text !== text) w.textSamples.push({ at: Date.now(), text });
        };
        new MutationObserver(read).observe(document.body, { subtree: true, childList: true, characterData: true });
      });

      const composer = page.locator("textarea.work-input");
      await composer.fill("How did weekly sales move?");
      await composer.press("Enter");

      await tablePart;
      await expect(page.locator(".work-transcript")).toContainText("Week 12 sales held steady");
      const samples = await page.evaluate(
        () => (window as unknown as { textSamples: Array<{ at: number; text: string }> }).textSamples,
      );
      const growth = samples.filter(
        (s, i) => i > 0 && s.at <= proseEndedAt && s.text.length > samples[i - 1]!.text.length && s.text.includes("Week"),
      );
      expect(growth.length, `text growth steps before the prose ended: ${growth.length}`).toBeGreaterThanOrEqual(3);

      const placeholder = page.locator(".work-block-pending[data-block='table']");
      await expect(placeholder).toBeVisible();
      await expect(placeholder).toContainText("Writing a table");
      await expect(page.locator(".work-transcript table")).toHaveCount(0);
      await expect(page.locator(".work-transcript")).not.toContainText("| Region");
      expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(400);

      releaseTable();
      await expect(page.locator(".work-transcript")).toContainText(AFTER);
      const table = page.locator(".work-transcript table");
      await expect(table).toHaveCount(1);
      await expect(table.locator("tbody tr")).toHaveCount(2);

      await cardPart;
      await expect(page.locator(".work-block-pending[data-block='card']")).toContainText("Preparing a card");
      await expect(page.locator(".work-block-pending[data-block='table']")).toHaveCount(0);
      await expect(page.locator(".work-transcript")).not.toContainText("Hidden payload");
      await expect(page.locator(".work-transcript")).not.toContainText("neko_cards");
      releaseCard();
      await expect(page.locator(".work-status-row")).toHaveCount(0);
      await expect(page.locator(".work-block-pending")).toHaveCount(0);
      await expect(page.locator(".work-transcript")).not.toContainText("Hidden payload");
      await context.close();
    } catch (err) {
      console.error(logs.slice(-4000));
      throw err;
    } finally {
      releaseTable();
      releaseCard();
      srv.kill("SIGTERM");
      serve.close();
      await client.query("delete from work_thread where id = $1", [threadId]).catch(() => {});
      await client.end();
      rmSync(work, { recursive: true, force: true });
    }
  });
});
