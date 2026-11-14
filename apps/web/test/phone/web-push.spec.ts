import { execFileSync, spawn } from "node:child_process";
import {
  createDecipheriv,
  createECDH,
  createHmac,
  randomBytes,
} from "node:crypto";
import {
  cpSync,
  existsSync,
  mkdtempSync,
  readFileSync,
  rmSync,
} from "node:fs";
import { createServer as createHttpServer, type IncomingMessage, type ServerResponse } from "node:http";
import { createServer as createNetServer } from "node:net";
import { tmpdir } from "node:os";
import path from "node:path";
import { expect, test, type Page, type Worker } from "@playwright/test";
import pg from "pg";

function resolveDatabaseUrl(): string {
  const file = process.env.OPENNEKO_DATABASE_URL_FILE;
  if (file) {
    return readFileSync(file, "utf8").trim();
  }
  if (process.env.DATABASE_URL) {
    return process.env.DATABASE_URL;
  }
  throw new Error("OPENNEKO_DATABASE_URL_FILE or DATABASE_URL is required");
}

async function freePort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const srv = createNetServer();
    srv.listen(0, "127.0.0.1", () => {
      const addr = srv.address();
      if (!addr || typeof addr === "string") {
        srv.close();
        reject(new Error("failed to allocate port"));
        return;
      }
      const { port } = addr;
      srv.close(() => resolve(port));
    });
    srv.on("error", reject);
  });
}

async function waitForController(page: Page): Promise<void> {
  await page.evaluate(async () => {
    if (!("serviceWorker" in navigator)) {
      throw new Error("serviceWorker is not supported");
    }
    await navigator.serviceWorker.ready;
    if (navigator.serviceWorker.controller) return;
    await new Promise<void>((resolve) => {
      navigator.serviceWorker.addEventListener("controllerchange", () => resolve(), { once: true });
    });
  });
}

function hmacSha256(key: Buffer, data: Buffer): Buffer {
  return createHmac("sha256", key).update(data).digest();
}

function decryptAes128GcmWebPush(
  body: Buffer,
  uaECDH: ReturnType<typeof createECDH>,
  uaPublic: Buffer,
  authSecret: Buffer,
): string {
  if (body.length < 21 + 65 + 17) {
    throw new Error(`encrypted body too short: ${body.length}`);
  }
  const salt = body.subarray(0, 16);
  const idlen = body[20]!;
  if (idlen !== 65) {
    throw new Error(`unexpected keyid length: ${idlen}`);
  }
  const asPublic = body.subarray(21, 21 + idlen);
  const ciphertextAndTag = body.subarray(21 + idlen);

  const ecdhSecret = uaECDH.computeSecret(asPublic);
  const prkKey = hmacSha256(authSecret, ecdhSecret);
  const keyInfo = Buffer.concat([
    Buffer.from("WebPush: info\0", "utf8"),
    uaPublic,
    asPublic,
    Buffer.from([1]),
  ]);
  const ikm = hmacSha256(prkKey, keyInfo);

  const prk = hmacSha256(salt, ikm);
  const cek = hmacSha256(
    prk,
    Buffer.from("Content-Encoding: aes128gcm\0\x01", "binary"),
  ).subarray(0, 16);
  const nonce = hmacSha256(
    prk,
    Buffer.from("Content-Encoding: nonce\0\x01", "binary"),
  ).subarray(0, 12);

  const ciphertext = ciphertextAndTag.subarray(0, ciphertextAndTag.length - 16);
  const tag = ciphertextAndTag.subarray(ciphertextAndTag.length - 16);
  const decipher = createDecipheriv("aes-128-gcm", cek, nonce);
  decipher.setAuthTag(tag);
  let padded = Buffer.concat([decipher.update(ciphertext), decipher.final()]);
  while (padded.length > 0 && padded[padded.length - 1] === 0x00) {
    padded = padded.subarray(0, padded.length - 1);
  }
  if (padded.length === 0 || padded[padded.length - 1] !== 0x02) {
    throw new Error("missing RFC 8291 0x02 record delimiter");
  }
  return padded.subarray(0, padded.length - 1).toString("utf8");
}

type PushNotice = {
  title: string;
  body: string;
  url: string;
  event: string;
};

type ReceivedPush = {
  path: string;
  authorization: string;
  cryptoKey: string;
  contentEncoding: string;
  ttl: string;
  rawJson: string;
  notice: PushNotice;
};

test.describe("4.4 Web Push for approval waiting, run finished, and workflow needs attention", () => {
  test.use({ viewport: { width: 400, height: 860 } });

  test("opts in on /profile, delivers the three fixed notices via River to a local push stub, handles them in sw.js, removes 410 Gone endpoints, and opts out", async ({
    browser,
  }) => {
    const webRoot = path.resolve(__dirname, "../..");
    const repoRoot = path.resolve(webRoot, "../..");
    const work = mkdtempSync(path.join(tmpdir(), "openneko-webpush-"));
    const configHome = path.join(work, "config");
    if (process.env.XDG_CONFIG_HOME && existsSync(process.env.XDG_CONFIG_HOME)) {
      cpSync(process.env.XDG_CONFIG_HOME, configHome, { recursive: true });
      rmSync(path.join(configHome, "openneko", "secrets.json"), { force: true });
    }

    const uaECDH = createECDH("prime256v1");
    const uaPublic = uaECDH.generateKeys();
    const authSecret = randomBytes(16);
    const p256dhB64 = uaPublic.toString("base64url");
    const authB64 = authSecret.toString("base64url");

    const received: ReceivedPush[] = [];
    const stubErrors: string[] = [];
    const gonePaths = new Set<string>();

    const pushStub = createHttpServer((req: IncomingMessage, res: ServerResponse) => {
      const reqPath = req.url ?? "/";
      const chunks: Buffer[] = [];
      req.on("data", (c: Buffer) => chunks.push(c));
      req.on("end", () => {
        if (gonePaths.has(reqPath)) {
          res.writeHead(410, { "Content-Type": "text/plain" });
          res.end("Gone");
          return;
        }
        try {
          const rawBody = Buffer.concat(chunks);
          const rawJson = decryptAes128GcmWebPush(rawBody, uaECDH, uaPublic, authSecret);
          const notice = JSON.parse(rawJson) as PushNotice;
          received.push({
            path: reqPath,
            authorization: String(req.headers.authorization ?? ""),
            cryptoKey: String(req.headers["crypto-key"] ?? ""),
            contentEncoding: String(req.headers["content-encoding"] ?? ""),
            ttl: String(req.headers.ttl ?? ""),
            rawJson,
            notice,
          });
          res.writeHead(201);
          res.end();
        } catch (err) {
          const msg = err instanceof Error ? err.message : "decrypt error";
          stubErrors.push(msg);
          res.writeHead(400, { "Content-Type": "text/plain" });
          res.end(msg);
        }
      });
    });

    const pushPort = await new Promise<number>((resolve, reject) => {
      pushStub.listen(0, "127.0.0.1", () => {
        const addr = pushStub.address();
        if (!addr || typeof addr === "string") {
          reject(new Error("failed to bind push stub"));
          return;
        }
        resolve(addr.port);
      });
      pushStub.on("error", reject);
    });

    const endpoint = `http://127.0.0.1:${pushPort}/push/phone-1`;
    const webPort = await freePort();
    const binPath = path.join(work, "openneko");
    execFileSync("go", ["build", "-o", binPath, "./cmd/openneko"], {
      cwd: path.join(repoRoot, "apps/openneko"),
      stdio: "ignore",
    });

    const client = new pg.Client({ connectionString: resolveDatabaseUrl() });
    await client.connect();

    // Clear any leftover action_execute jobs from earlier tests before starting the job runner.
    await client.query(`delete from river.river_job where kind = 'action_execute'`).catch(() => {});

    const wakeToken = "test-webpush-wake-token";
    let serverLogs = "";
    const srv = spawn(
      binPath,
      ["web", "--addr", `127.0.0.1:${webPort}`, "--dir", path.join(webRoot, "dist"), "--jobs=false"],
      {
        env: {
          ...process.env,
          XDG_CONFIG_HOME: configHome,
          OPENNEKO_JOBS_WAKE_TOKEN: wakeToken,
        },
        stdio: ["ignore", "pipe", "pipe"],
      },
    );
    srv.stdout?.on("data", (d: Buffer) => {
      serverLogs += d.toString("utf8");
    });
    srv.stderr?.on("data", (d: Buffer) => {
      serverLogs += d.toString("utf8");
    });

    const origin = `http://127.0.0.1:${webPort}`;

    try {
      for (let i = 0; i < 40; i++) {
        try {
          const r = await fetch(`${origin}/api/v1/version`);
          if (r.ok) break;
        } catch {
          // Wait for server startup.
        }
        await new Promise((r) => setTimeout(r, 200));
      }

      // 1. Verify VAPID keys were generated on first start in the secrets store and never logged.
      const secretsFile = path.join(configHome, "openneko", "secrets.json");
      const secretsJson = JSON.parse(readFileSync(secretsFile, "utf8")) as {
        web_push?: {
          VAPID_PUBLIC_KEY?: string;
          VAPID_PRIVATE_KEY?: string;
        };
      };
      const vapidPub = secretsJson.web_push?.VAPID_PUBLIC_KEY ?? "";
      const vapidPriv = secretsJson.web_push?.VAPID_PRIVATE_KEY ?? "";
      expect(vapidPub.length).toBeGreaterThan(20);
      expect(vapidPriv.length).toBeGreaterThan(20);
      expect(serverLogs).not.toContain(vapidPub);
      expect(serverLogs).not.toContain(vapidPriv);

      const { rows: orgRows } = await client.query<{
        id: string;
        solo_admin_user_id: string | null;
      }>("select id, solo_admin_user_id from organization limit 1");
      const orgId = orgRows[0]!.id;
      let userId = orgRows[0]!.solo_admin_user_id;
      if (!userId) {
        const { rows: userRows } = await client.query<{ id: string }>(
          "select id from app_user where org_id = $1 order by created_at asc limit 1",
          [orgId],
        );
        userId = userRows[0]!.id;
      }

      const context = await browser.newContext({ viewport: { width: 400, height: 860 } });
      await context.grantPermissions(["notifications"], { origin });

      await context.addInitScript(
        ({ stubEndpoint, p256dh, auth }) => {
          let activeSub: {
            endpoint: string;
            expirationTime: null;
            options: { userVisibleOnly: boolean };
            getKey: (name: string) => ArrayBuffer | null;
            toJSON: () => { endpoint: string; expirationTime: null; keys: { p256dh: string; auth: string } };
            unsubscribe: () => Promise<boolean>;
          } | null = null;

          const decodeB64Url = (s: string): ArrayBuffer => {
            const pad = s.length % 4 === 0 ? "" : "=".repeat(4 - (s.length % 4));
            const bin = atob(s.replace(/-/g, "+").replace(/_/g, "/") + pad);
            const out = new Uint8Array(bin.length);
            for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
            return out.buffer;
          };

          const makeSub = () => ({
            endpoint: stubEndpoint,
            expirationTime: null,
            options: { userVisibleOnly: true },
            getKey(name: string) {
              if (name === "p256dh") return decodeB64Url(p256dh);
              if (name === "auth") return decodeB64Url(auth);
              return null;
            },
            toJSON() {
              return {
                endpoint: stubEndpoint,
                expirationTime: null,
                keys: { p256dh, auth },
              };
            },
            async unsubscribe() {
              activeSub = null;
              return true;
            },
          });

          const fakePushManager = {
            async getSubscription() {
              return activeSub;
            },
            async subscribe() {
              activeSub = makeSub();
              return activeSub;
            },
            async permissionState() {
              return "granted";
            },
          };

          Object.defineProperty(ServiceWorkerRegistration.prototype, "pushManager", {
            configurable: true,
            get() {
              return fakePushManager;
            },
          });
        },
        { stubEndpoint: endpoint, p256dh: p256dhB64, auth: authB64 },
      );

      const page = await context.newPage();
      const pageErrors: string[] = [];
      page.on("pageerror", (err) => pageErrors.push(err.message));

      // 2. Open /profile at 400 px width, verify Push notifications UI, and opt in for this device.
      await page.goto(`${origin}/profile`, { waitUntil: "networkidle" });
      await waitForController(page);

      let swWorker: Worker | undefined = context
        .serviceWorkers()
        .find((w) => w.url().startsWith(origin));
      if (!swWorker) {
        swWorker = await context.waitForEvent("serviceworker", (w) => w.url().startsWith(origin));
      }

      await expect(page.getByRole("heading", { name: "Push notifications", exact: true })).toBeVisible();
      await expect(page.getByTestId("push-status")).toHaveText("Notifications are off in this browser.");
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= 400)).toBe(true);

      const listBeforeRes = await page.evaluate(async () => {
        const r = await fetch("/api/v1/my/push-subscriptions");
        return await r.text();
      });
      expect(listBeforeRes).toBe('{"subscriptions":[]}');
      expect(listBeforeRes).not.toContain(vapidPub);
      expect(listBeforeRes).not.toContain(vapidPriv);

      await page.getByRole("button", { name: "Turn on notifications" }).click();
      await expect(page.getByTestId("push-status")).toHaveText("Notifications are on in this browser.");
      await expect(page.getByRole("button", { name: "Turn off notifications" })).toBeVisible();
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= 400)).toBe(true);

      const subRows = await client.query<{ id: string; endpoint: string; user_id: string }>(
        `select id::text as id, endpoint, user_id from app_push_subscription where org_id = $1 and user_id = $2`,
        [orgId, userId],
      );
      expect(subRows.rows).toHaveLength(1);
      expect(subRows.rows[0]!.endpoint).toBe(endpoint);

      // Listen for OPENNEKO_PUSH_NOTICE messages from sw.js in the page.
      await page.evaluate(() => {
        (window as unknown as { __pushNotices?: PushNotice[] }).__pushNotices = [];
        navigator.serviceWorker.addEventListener("message", (ev) => {
          if (ev.data?.type === "OPENNEKO_PUSH_NOTICE") {
            (window as unknown as { __pushNotices: PushNotice[] }).__pushNotices.push({
              title: String(ev.data.title ?? ""),
              body: String(ev.data.body ?? ""),
              url: String(ev.data.url ?? ""),
              event: String(ev.data.event ?? ""),
            });
          }
        });
      });

      // 3. Queue the three Web Push events as River jobs and verify openneko web delivers them to the stub.
      for (const eventName of [
        "approval_waiting",
        "run_finished",
        "workflow_needs_attention",
      ]) {
        await client.query(
          `insert into river.river_job (kind, args, max_attempts, state, queue)
           values ('web_push', $1::jsonb, 4, 'available', 'default')`,
          [JSON.stringify({ orgId, event: eventName, users: [userId] })],
        );
      }

      const wakeRes = await fetch(`${origin}/internal/jobs/wake?max_seconds=10`, {
        method: "POST",
        headers: { Authorization: `Bearer ${wakeToken}` },
      });
      const wakeBody = await wakeRes.json();
      const jobRows = await client.query(
        `select id::text, state::text, attempt, errors, args from river.river_job where kind = 'web_push' order by id asc`,
      );
      expect({ status: wakeRes.status, wakeBody, stubErrors, jobs: jobRows.rows }).toMatchObject({
        status: 200,
        stubErrors: [],
      });

      await expect
        .poll(() => received.map((r) => r.notice.event).sort(), { timeout: 5_000 })
        .toEqual(["approval_waiting", "run_finished", "workflow_needs_attention"]);

      for (const p of received) {
        expect(p.path).toBe("/push/phone-1");
        expect(p.contentEncoding).toBe("aes128gcm");
        expect(p.ttl).toBe("86400");
        const kMatch = p.authorization.match(/^vapid t=[^,]+, k=([A-Za-z0-9_-]{80,90})$/);
        expect(kMatch?.[1]).toBeTruthy();
        expect(serverLogs).not.toContain(kMatch![1]!);
        expect(listBeforeRes).not.toContain(kMatch![1]!);
        expect(Object.keys(JSON.parse(p.rawJson) as Record<string, unknown>).sort()).toEqual([
          "body",
          "event",
          "title",
          "url",
        ]);
      }

      const byEvent = new Map(received.map((r) => [r.notice.event, r]));
      expect(byEvent.get("approval_waiting")?.notice).toEqual({
        title: "OpenNeko",
        body: "An approval is waiting.",
        url: "/actions",
        event: "approval_waiting",
      });
      expect(byEvent.get("run_finished")?.notice).toEqual({
        title: "OpenNeko",
        body: "A run finished.",
        url: "/runs",
        event: "run_finished",
      });
      expect(byEvent.get("workflow_needs_attention")?.notice).toEqual({
        title: "OpenNeko",
        body: "A workflow needs attention.",
        url: "/workflows",
        event: "workflow_needs_attention",
      });

      // 4. Deliver each decrypted payload into the active Service Worker (`sw.js`) and verify notification + click link.
      for (const expected of [
        { event: "approval_waiting", body: "An approval is waiting.", url: "/actions" },
        { event: "run_finished", body: "A run finished.", url: "/runs" },
        { event: "workflow_needs_attention", body: "A workflow needs attention.", url: "/workflows" },
      ]) {
        const pushed = byEvent.get(expected.event)!;
        await swWorker.evaluate(async (rawJson) => {
          const swGlobal = globalThis as unknown as {
            PushEvent: new (type: string, init: { data: string }) => Event;
            dispatchEvent: (event: Event) => boolean;
          };
          const ev = new swGlobal.PushEvent("push", { data: rawJson });
          swGlobal.dispatchEvent(ev);
        }, pushed.rawJson);

        await expect
          .poll(
            () =>
              page.evaluate(
                (evName) =>
                  (
                    (window as unknown as { __pushNotices?: PushNotice[] }).__pushNotices ?? []
                  ).find((n) => n.event === evName) ?? null,
                expected.event,
              ),
            { timeout: 5_000 },
          )
          .toEqual({
            title: "OpenNeko",
            body: expected.body,
            url: expected.url,
            event: expected.event,
          });
      }

      // 5. Mark the push endpoint 410 Gone at the stub and verify the River job removes the subscription.
      gonePaths.add("/push/phone-1");
      await client.query(
        `insert into river.river_job (kind, args, max_attempts, state, queue)
         values ('web_push', $1::jsonb, 4, 'available', 'default')`,
        [JSON.stringify({ orgId, event: "run_finished", users: [userId] })],
      );

      const wakeGoneRes = await fetch(`${origin}/internal/jobs/wake?max_seconds=10`, {
        method: "POST",
        headers: { Authorization: `Bearer ${wakeToken}` },
      });
      expect(wakeGoneRes.status).toBe(200);

      await expect
        .poll(
          async () => {
            const r = await client.query<{ count: string }>(
              `select count(*)::text as count from app_push_subscription where org_id = $1 and user_id = $2`,
              [orgId, userId],
            );
            return Number(r.rows[0]?.count ?? "0");
          },
          { timeout: 5_000 },
        )
        .toBe(0);

      // 6. Reload /profile, re-enable, and then turn off from the profile UI.
      gonePaths.delete("/push/phone-1");
      await page.reload({ waitUntil: "networkidle" });
      await expect(page.getByTestId("push-status")).toHaveText("Notifications are off in this browser.");

      await page.getByRole("button", { name: "Turn on notifications" }).click();
      await expect(page.getByTestId("push-status")).toHaveText("Notifications are on in this browser.");

      await page.getByRole("button", { name: "Turn off notifications" }).click();
      await expect(page.getByTestId("push-status")).toHaveText("Notifications are off in this browser.");

      const afterManualOff = await client.query<{ count: string }>(
        `select count(*)::text as count from app_push_subscription where org_id = $1 and user_id = $2`,
        [orgId, userId],
      );
      expect(Number(afterManualOff.rows[0]?.count ?? "0")).toBe(0);
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= 400)).toBe(true);
      expect(pageErrors).toEqual([]);
      expect(serverLogs).not.toContain(vapidPub);
      expect(serverLogs).not.toContain(vapidPriv);

      await context.close();
    } finally {
      srv.kill("SIGTERM");
      pushStub.close();
      await client.query(`delete from app_push_subscription`).catch(() => {});
      await client.end();
      rmSync(work, { recursive: true, force: true });
    }
  });
});
