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
  mkdirSync,
  mkdtempSync,
  readFileSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import http from "node:http";
import https from "node:https";
import net from "node:net";
import { tmpdir } from "node:os";
import path from "node:path";
import pg from "pg";
import { verifyVapidForSubscription } from "./vapid";
import { expect, test, type Page } from "@playwright/test";

function resolveDatabaseUrl(): string {
  const file = process.env.OPENNEKO_DATABASE_URL_FILE;
  if (file) return readFileSync(file, "utf8").trim();
  const direct = process.env.DATABASE_URL;
  if (direct) return direct.trim();
  throw new Error(
    "set OPENNEKO_DATABASE_URL_FILE or DATABASE_URL (scripts/phone-local.sh)",
  );
}

function freePort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const s = net.createServer();
    s.on("error", reject);
    s.listen(0, "127.0.0.1", () => {
      const addr = s.address();
      if (!addr || typeof addr === "string") {
        s.close();
        reject(new Error("no tcp address"));
        return;
      }
      const { port } = addr;
      s.close((err) => (err ? reject(err) : resolve(port)));
    });
  });
}

async function waitReady(url: string, timeoutMs = 20_000): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    try {
      const res = await fetch(`${url}/api/v1/version`);
      if (res.ok) return;
    } catch {
      // Server still starting.
    }
    await new Promise((r) => setTimeout(r, 200));
  }
  throw new Error(`openneko web at ${url} did not become ready in ${timeoutMs}ms`);
}

function hkdfExtract(salt: Buffer, ikm: Buffer): Buffer {
  const key = salt.length > 0 ? salt : Buffer.alloc(32, 0);
  return createHmac("sha256", key).update(ikm).digest();
}

function hkdfExpand32(prk: Buffer, info: Buffer, length: number): Buffer {
  const t1 = createHmac("sha256", prk)
    .update(Buffer.concat([info, Buffer.from([1])]))
    .digest();
  return t1.subarray(0, length);
}

function decryptWebPushAes128Gcm(
  body: Buffer,
  uaEcdh: ReturnType<typeof createECDH>,
  uaPublic: Buffer,
  authSecret: Buffer,
): string {
  if (body.length < 21 + 16) {
    throw new Error(`aes128gcm body too short (${body.length} bytes)`);
  }
  const salt = body.subarray(0, 16);
  const idlen = body[20]!;
  if (idlen !== 65 || body.length < 21 + idlen + 17) {
    throw new Error(`unexpected keyid length ${idlen}`);
  }
  const asPublic = body.subarray(21, 21 + idlen);
  const ciphertextAndTag = body.subarray(21 + idlen);
  const ciphertext = ciphertextAndTag.subarray(0, ciphertextAndTag.length - 16);
  const tag = ciphertextAndTag.subarray(ciphertextAndTag.length - 16);

  const ecdhSecret = uaEcdh.computeSecret(asPublic);
  const keyInfo = Buffer.concat([
    Buffer.from("WebPush: info\x00", "ascii"),
    uaPublic,
    asPublic,
  ]);
  const ikm = hkdfExpand32(hkdfExtract(authSecret, ecdhSecret), keyInfo, 32);
  const prk = hkdfExtract(salt, ikm);
  const cek = hkdfExpand32(
    prk,
    Buffer.from("Content-Encoding: aes128gcm\x00", "ascii"),
    16,
  );
  const nonce = hkdfExpand32(
    prk,
    Buffer.from("Content-Encoding: nonce\x00", "ascii"),
    12,
  );

  const decipher = createDecipheriv("aes-128-gcm", cek, nonce);
  decipher.setAuthTag(tag);
  const padded = Buffer.concat([decipher.update(ciphertext), decipher.final()]);
  let end = padded.length;
  while (end > 0 && padded[end - 1] === 0x00) end--;
  if (end === 0 || padded[end - 1] !== 0x02) {
    throw new Error("missing RFC 8291 0x02 record delimiter");
  }
  return padded.subarray(0, end - 1).toString("utf8");
}

function pngDimensions(buf: Buffer): { width: number; height: number } {
  const sig = Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]);
  expect(buf.subarray(0, 8).equals(sig)).toBe(true);
  expect(buf.subarray(12, 16).toString("ascii")).toBe("IHDR");
  return {
    width: buf.readUInt32BE(16),
    height: buf.readUInt32BE(20),
  };
}

async function assertNoHorizontalOverflow(page: Page, label: string): Promise<void> {
  const metrics = await page.evaluate(() => {
    window.scrollTo(10_000, window.scrollY);
    const width = document.documentElement.clientWidth;
    const inside = (b: DOMRect) => b.left >= -1 && b.right <= width + 1;
    const pastEdge: string[] = [];
    for (const el of document.querySelectorAll("body *")) {
      const box = el.getBoundingClientRect();
      if (box.width === 0 || box.height === 0 || inside(box)) continue;
      let scrolls = false;
      for (let p = el.parentElement; p && p !== document.body; p = p.parentElement) {
        if (
          /(auto|scroll|hidden|clip)/.test(getComputedStyle(p).overflowX) &&
          inside(p.getBoundingClientRect())
        ) {
          scrolls = true;
          break;
        }
      }
      if (!scrolls) {
        pastEdge.push(
          `${el.tagName.toLowerCase()}.${String(el.className).slice(0, 60)}`,
        );
      }
    }
    return {
      innerWidth: window.innerWidth,
      scrollWidth: document.documentElement.scrollWidth,
      scrollX: window.scrollX,
      pastEdge,
    };
  });
  expect(metrics.innerWidth, `${label}: innerWidth`).toBe(400);
  expect(metrics.scrollWidth, `${label}: html scrollWidth`).toBeLessThanOrEqual(400);
  expect(metrics.scrollX, `${label}: window.scrollX`).toBe(0);
  expect(metrics.pastEdge, `${label}: contentPastEdge`).toEqual([]);
}

interface ReceivedPush {
  path: string;
  headers: http.IncomingHttpHeaders;
  rawJson: string;
  notice: {
    title: string;
    body: string;
    url: string;
    event: string;
    [k: string]: unknown;
  };
}

const SSO_GROUP_ID = "c0a7ac70-0000-4000-8000-000000000491";
const IDP_RULE_ID = "c0a7ac70-0000-4000-8000-000000000492";
const DATA_SOURCE_ID = "c0a7ac70-0000-4000-8000-000000000493";
const POLICY_ID = "c0a7ac70-0000-4000-8000-000000000494";
const PACK_ACT_DEF_ID = "c0a7ac70-0000-4000-8000-000000000495";
const PACK_ARTIFACT_ID = "c0a7ac70-0000-4000-8000-000000000496";
const SECOND_AR_ID = "c0a7ac70-0000-4000-8000-000000000497";

// The headless shell refuses notification permission inside a service worker.
test.use({ channel: "chromium" });

test.describe("Phase 4 gate check (G4)", () => {
  test("new user signs in, finishes onboarding, verifies PWA installability, streams and resumes on reload against the scripted model, enables Web Push, registers a passkey, receives a zero-data push notice on an action request, approves with passkey into the audit log, and gets 403 without a passkey", async ({
    browser,
  }) => {
    const dbUrl = resolveDatabaseUrl();
    const client = new pg.Client({ connectionString: dbUrl });
    await client.connect();

    const webRoot = path.resolve(__dirname, "../..");
    const repoRoot = path.resolve(webRoot, "../..");
    const distSource = path.join(webRoot, "dist");
    const work = mkdtempSync(path.join(tmpdir(), "neko-gate4-"));
    const distDir = path.join(work, "dist");
    cpSync(distSource, distDir, { recursive: true });

    const binPath =
      process.env.OPENNEKO_PHONE_BIN && existsSync(process.env.OPENNEKO_PHONE_BIN)
        ? process.env.OPENNEKO_PHONE_BIN
        : path.join(work, "openneko");
    if (!existsSync(binPath)) {
      execFileSync("go", ["build", "-o", binPath, "./cmd/openneko"], {
        cwd: path.join(repoRoot, "apps/openneko"),
        stdio: "inherit",
      });
    }

    const uaEcdh = createECDH("prime256v1");
    const uaPublic = uaEcdh.generateKeys();
    const authSecret = randomBytes(16);
    const p256dhB64 = uaPublic.toString("base64url");
    const authB64 = authSecret.toString("base64url");
    const receivedPushes: ReceivedPush[] = [];

    const orgRes = await client.query<{ id: string }>(
      `select id from organization limit 1`,
    );
    const orgId = orgRes.rows[0]!.id;

    const certPath = path.join(work, "scalekit-cert.pem");
    const keyPath = path.join(work, "scalekit-key.pem");
    execFileSync(
      "openssl",
      [
        "req",
        "-x509",
        "-newkey",
        "rsa:2048",
        "-nodes",
        "-keyout",
        keyPath,
        "-out",
        certPath,
        "-days",
        "1",
        "-subj",
        "/CN=127.0.0.1",
        "-addext",
        "subjectAltName=IP:127.0.0.1",
      ],
      { stdio: "ignore" },
    );

    const scalekitStub = https.createServer(
      {
        key: readFileSync(keyPath),
        cert: readFileSync(certPath),
      },
      (req, res) => {
        const reqUrl = new URL(req.url ?? "/", "https://127.0.0.1");
        if (req.method === "GET" && reqUrl.pathname === "/oauth/authorize") {
          const redirectUri = reqUrl.searchParams.get("redirect_uri") ?? "";
          const state = reqUrl.searchParams.get("state") ?? "";
          const sep = redirectUri.includes("?") ? "&" : "?";
          res.writeHead(302, {
            Location: `${redirectUri}${sep}code=gate4-oidc-code&state=${encodeURIComponent(state)}`,
          });
          res.end();
          return;
        }
        if (req.method === "POST" && reqUrl.pathname === "/oauth/token") {
          res.writeHead(200, { "Content-Type": "application/json" });
          res.end(JSON.stringify({ access_token: "gate4-access-token" }));
          return;
        }
        if (req.method === "GET" && reqUrl.pathname === "/userinfo") {
          res.writeHead(200, { "Content-Type": "application/json" });
          res.end(
            JSON.stringify({
              sub: "gate4-oidc-sub-alex",
              email: "alex.rivera@acme.test",
              name: "Alex Rivera",
              organization_id: orgId,
              groups: ["idp-admins"],
            }),
          );
          return;
        }
        res.writeHead(404, { "Content-Type": "application/json" });
        res.end(JSON.stringify({ error: "not found" }));
      },
    );

    const scalekitPort = await new Promise<number>((resolve, reject) => {
      scalekitStub.on("error", reject);
      scalekitStub.listen(0, "127.0.0.1", () => {
        const addr = scalekitStub.address();
        if (!addr || typeof addr === "string") {
          reject(new Error("no scalekit port"));
          return;
        }
        resolve(addr.port);
      });
    });

    // Stub server for Web Push delivery (/push/*) and OpenAI-compatible host
    // calls (/v1/chat/completions).
    const httpStub = http.createServer((req, res) => {
      const chunks: Buffer[] = [];
      req.on("data", (c: Buffer) => chunks.push(c));
      req.on("end", () => {
        const rawBody = Buffer.concat(chunks);
        const urlPath = req.url ?? "/";

        if (req.method === "POST" && urlPath.startsWith("/push/")) {
          try {
            const rawJson = decryptWebPushAes128Gcm(
              rawBody,
              uaEcdh,
              uaPublic,
              authSecret,
            );
            const notice = JSON.parse(rawJson) as ReceivedPush["notice"];
            receivedPushes.push({
              path: urlPath,
              headers: req.headers,
              rawJson,
              notice,
            });
            res.writeHead(201);
            res.end();
          } catch (err) {
            res.writeHead(400);
            res.end(String(err));
          }
          return;
        }

        if (req.method === "POST" && urlPath.endsWith("/chat/completions")) {
          res.writeHead(200, { "Content-Type": "application/json" });
          res.end(
            JSON.stringify({
              id: "chatcmpl-scripted",
              object: "chat.completion",
              choices: [
                {
                  index: 0,
                  message: {
                    role: "assistant",
                    content: JSON.stringify({
                      changed: false,
                      instructions: "",
                    }),
                  },
                  finish_reason: "stop",
                },
              ],
              usage: {
                prompt_tokens: 12,
                completion_tokens: 6,
                total_tokens: 18,
              },
            }),
          );
          return;
        }

        res.writeHead(404, { "Content-Type": "application/json" });
        res.end(JSON.stringify({ error: "not found" }));
      });
    });

    const stubPort = await new Promise<number>((resolve, reject) => {
      httpStub.on("error", reject);
      httpStub.listen(0, "127.0.0.1", () => {
        const addr = httpStub.address();
        if (!addr || typeof addr === "string") {
          reject(new Error("no stub port"));
          return;
        }
        resolve(addr.port);
      });
    });

    // Scripted model server on OPENNEKO_SERVE_SOCKET.
    let resolveFirstChunkSent!: () => void;
    const firstChunkSent = new Promise<void>((r) => {
      resolveFirstChunkSent = r;
    });
    let releaseSecondChunk!: () => void;
    const waitForReloadRelease = new Promise<void>((r) => {
      releaseSecondChunk = r;
    });

    const firstHalfText = "First half of the scripted answer is streaming live. ";
    const secondHalfText =
      "Second half arrived after reload and completed the turn.";
    const fullStreamedAnswer = `${firstHalfText}${secondHalfText}`;

    const actionFenceTurnText = [
      "I have queued the supplier wire hold for your approval.",
      "",
      "```neko_action_request",
      JSON.stringify({
        scope: "external",
        kind: "gate4.supplier_hold",
        target: "VENDOR-SECRET-99",
        risk_level: "high",
        summary: "Hold $48,500 wire to Acme Industrial Supplies for SKU-999",
        payload: {
          operation: "hold_payment",
          vendor: "Acme Industrial Supplies",
          amount_usd: 48500,
          sku: "SKU-999",
        },
      }),
      "```",
    ].join(String.fromCharCode(10));

    const NL = String.fromCharCode(10);
    const serveSocketPath = path.join(work, "serve.sock");
    const serveSocketServer = net.createServer((conn) => {
      let buf = "";
      conn.on("data", (chunk: Buffer) => {
        buf += chunk.toString("utf8");
        const nl = buf.indexOf(NL);
        if (nl < 0) return;
        const firstLine = buf.slice(0, nl).trim();
        buf = buf.slice(nl + 1);
        if (!firstLine) return;

        void (async () => {
          const msg = JSON.parse(firstLine) as {
            type?: string;
            spec?: {
              run_id?: string;
              prompt?: string;
              skill_query?: string;
            };
          };
          if (msg.type !== "start" || !msg.spec?.run_id) {
            conn.end();
            return;
          }
          const runId = msg.spec.run_id;
          const prompt = msg.spec.prompt ?? "";
          const userQuery = msg.spec.skill_query ?? "";

          const sendFrame = (frame: unknown) => {
            if (!conn.destroyed) {
              conn.write(`${JSON.stringify(frame)}${NL}`);
            }
          };

          if (runId.startsWith("ax-profiler-") || prompt.includes("Business Profile")) {
            const profileMd = [
              "# Acme Outdoor — Business Profile",
              "",
              "## What they are",
              "Acme Outdoor supplies technical outdoor gear across wholesale and direct channels.",
              "",
              "## Who they serve",
              "Specialty outdoor retailers and direct ecommerce customers.",
            ].join(NL);
            sendFrame({
              type: "event",
              event: {
                version: 1,
                run_id: runId,
                sequence: 1,
                type: "answer.delta",
                data: { text: profileMd, version: 1 },
              },
            });
            sendFrame({
              type: "event",
              event: {
                version: 1,
                run_id: runId,
                sequence: 2,
                type: "run.finished",
                result: {
                  status: "completed",
                  answer: profileMd,
                  usage: {
                    coverage: "complete",
                    requests: 1,
                    reported: 1,
                    input_tokens: 30,
                    output_tokens: 25,
                    total_tokens: 55,
                  },
                },
              },
            });
            sendFrame({ type: "exit", code: 0 });
            conn.end();
            return;
          }

          if (userQuery.includes("SKU-999") || prompt.includes("SKU-999")) {
            sendFrame({
              type: "event",
              event: {
                version: 1,
                run_id: runId,
                sequence: 1,
                type: "answer.delta",
                data: {
                  text: "I have queued the supplier wire hold for your approval.",
                  version: 1,
                },
              },
            });
            sendFrame({
              type: "event",
              event: {
                version: 1,
                run_id: runId,
                sequence: 2,
                type: "run.finished",
                result: {
                  status: "completed",
                  answer: actionFenceTurnText,
                  usage: {
                    coverage: "complete",
                    requests: 1,
                    reported: 1,
                    input_tokens: 45,
                    output_tokens: 35,
                    total_tokens: 80,
                  },
                },
              },
            });
            sendFrame({ type: "exit", code: 0 });
            conn.end();
            return;
          }

          // First question: stream chunk 1, wait for browser reload, then stream chunk 2.
          sendFrame({
            type: "event",
            event: {
              version: 1,
              run_id: runId,
              sequence: 1,
              type: "answer.delta",
              data: { text: firstHalfText, version: 1 },
            },
          });
          resolveFirstChunkSent();
          await waitForReloadRelease;
          sendFrame({
            type: "event",
            event: {
              version: 1,
              run_id: runId,
              sequence: 2,
              type: "answer.delta",
              data: { text: secondHalfText, version: 1 },
            },
          });
          sendFrame({
            type: "event",
            event: {
              version: 1,
              run_id: runId,
              sequence: 3,
              type: "run.finished",
              result: {
                status: "completed",
                answer: fullStreamedAnswer,
                usage: {
                  coverage: "complete",
                  requests: 1,
                  reported: 1,
                  input_tokens: 40,
                  output_tokens: 24,
                  total_tokens: 64,
                },
              },
            },
          });
          sendFrame({ type: "exit", code: 0 });
          conn.end();
        })();
      });
    });

    await new Promise<void>((resolve, reject) => {
      serveSocketServer.on("error", reject);
      serveSocketServer.listen(serveSocketPath, () => resolve());
    });

    // Configure secrets.json so `/signin` and Scalekit SSO are active in Go.
    const configHome = path.join(work, "config");
    const opennekoConfigDir = path.join(configHome, "openneko");
    mkdirSync(opennekoConfigDir, { recursive: true });
    writeFileSync(
      path.join(opennekoConfigDir, "secrets.json"),
      JSON.stringify({
        signin: {
          PROVIDER: "scalekit",
        },
        "signin.scalekit": {
          SCALEKIT_ENVIRONMENT_URL: `https://127.0.0.1:${scalekitPort}`,
          SCALEKIT_CLIENT_ID: "gate4-client-id",
          SCALEKIT_CLIENT_SECRET: "gate4-client-secret",
        },
      }),
      { mode: 0o600 },
    );

    // Seed LLM provider config, default data source, IdP admin group rule,
    // and Go-owned action definition + require_approval policy.
    const adminGroupRes = await client.query<{ id: string }>(
      `select id::text from user_group where org_id = $1 and slug = 'administrators' limit 1`,
      [orgId],
    );
    const adminGroupId = adminGroupRes.rows[0]!.id;
    const packInstallRes = await client.query<{ id: string }>(
      `select id::text from pack_install where org_id = $1 limit 1`,
      [orgId],
    );
    const packInstallId = packInstallRes.rows[0]!.id;
    await client.query(
      `update pack_install
          set config = coalesce(config, '{}'::jsonb) || '{"_runtime":{"source":{"dataSourceId":"warehouse"}}}'::jsonb
        where id = $1`,
      [packInstallId],
    );

    await client.query(
      `insert into sso_group (id, org_id, provider, tenant_id, external_id, display_name, active)
       values ($1, $2, 'scalekit', $2, 'idp-admins', 'Administrators', true)
       on conflict (id) do nothing`,
      [SSO_GROUP_ID, orgId],
    );
    await client.query(
      `insert into idp_group_rule (id, org_id, sso_group_id, user_group_id)
       values ($1, $2, $3, $4)
       on conflict (id) do nothing`,
      [IDP_RULE_ID, orgId, SSO_GROUP_ID, adminGroupId],
    );
    await client.query(
      `insert into llm_provider_config (org_id, scope, provider, model, enabled, config, secrets)
       values
         ($1, 'primary', 'openai-compatible', 'scripted-v1', true, $2::jsonb, '{"apiKey":"scripted-key"}'::jsonb),
         ($1, 'research', 'none', null, false, '{}'::jsonb, '{}'::jsonb)
       on conflict (org_id, scope) do update set
         provider = excluded.provider,
         model = excluded.model,
         enabled = excluded.enabled,
         config = excluded.config,
         secrets = excluded.secrets`,
      [orgId, JSON.stringify({ url: `http://127.0.0.1:${stubPort}/v1` })],
    );
    await client.query(
      `insert into data_source (id, org_id, kind, name, graphql_url, mcp_url, auth_mode, is_default, enabled)
       values ($1, $2, 'postgres', 'Warehouse DB', $3, $4, 'none', true, true)
       on conflict (id) do nothing`,
      [
        DATA_SOURCE_ID,
        orgId,
        `http://127.0.0.1:${stubPort}/api/v1/graphql`,
        `http://127.0.0.1:${stubPort}/api/v1/mcp`,
      ],
    );
    await client.query(
      `insert into action_policy (id, org_id, name, description, applies_to_kinds, applies_to_scopes, mode, priority, enabled)
       values ($1, $2, 'Gate 4 supplier hold approval', 'Require approval for supplier holds', '{"gate4.supplier_hold"}', '{"external"}', 'approval_required', 1, true)
       on conflict (id) do nothing`,
      [POLICY_ID, orgId],
    );
    await client.query(
      `insert into pack_action_definition (id, org_id, kind, description, definition, definition_hash, readiness, enabled)
       values ($1, $2, 'gate4.supplier_hold', 'Hold supplier payment', $3::jsonb, 'gate4-hash', 'ready', true)
       on conflict (id) do nothing`,
      [
        PACK_ACT_DEF_ID,
        orgId,
        JSON.stringify({
          kind: "gate4.supplier_hold",
          description: "Hold supplier payment",
          adapter: {
            kind: "graphjin_api_operation",
            source: "warehouse",
            spec: "payments",
            operations: {
              hold_payment: {
                operationId: "holdPayment",
                method: "POST",
                path: "/payments/hold",
              },
            },
          },
        }),
      ],
    );
    await client.query(
      `insert into pack_artifact (id, org_id, pack_install_id, artifact_kind, artifact_key, target_ref, desired_hash, last_applied_hash, ownership, readiness)
       values ($1, $2, $3, 'action', 'gate4.supplier_hold', 'gate4.supplier_hold', 'gate4-hash', 'gate4-hash', 'managed', 'ready')
       on conflict (id) do nothing`,
      [PACK_ARTIFACT_ID, orgId, packInstallId],
    );

    // Start with customer_profile not current so the new user goes through both
    // the organization onboarding wizard (backed by the scripted model) and the
    // personal persona onboarding step.
    await client.query(
      `update customer_profile set is_current = false where org_id = $1`,
      [orgId],
    );

    const dbFile = path.join(work, "database-url");
    writeFileSync(dbFile, dbUrl, { mode: 0o600 });
    const wakeToken = "gate4-wake-token-secret";
    const sessionSecret = "gate4-session-secret-0123456789abcdef0123456789abcdef";
    const port = await freePort();
    const origin = `http://localhost:${port}`;

    let srvLogs = "";
    const srv = spawn(
      binPath,
      [
        "web",
        "--addr",
        `127.0.0.1:${port}`,
        "--dir",
        distDir,
        "--jobs=false",
      ],
      {
        env: {
          ...process.env,
          XDG_CONFIG_HOME: configHome,
          XDG_DATA_HOME: path.join(work, "data"),
          OPENNEKO_DATABASE_URL_FILE: dbFile,
          OPENNEKO_SERVE_SOCKET: serveSocketPath,
          OPENNEKO_WEB_PUSH_ALLOW_LOOPBACK: "1",
          OPENNEKO_JOBS_WAKE_TOKEN: wakeToken,
          OPENNEKO_SESSION_SECRET: sessionSecret,
          SSL_CERT_FILE: certPath,
        },
        stdio: ["ignore", "pipe", "pipe"],
      },
    );
    srv.stdout?.on("data", (d: Buffer) => {
      srvLogs += d.toString("utf8");
    });
    srv.stderr?.on("data", (d: Buffer) => {
      srvLogs += d.toString("utf8");
    });

    const wakeJobs = async () => {
      const res = await fetch(`${origin}/internal/jobs/wake?max_seconds=10`, {
        method: "POST",
        headers: {
          Authorization: `Bearer ${wakeToken}`,
        },
      });
      expect(res.status).toBe(200);
    };

    let newUserId: string | null = null;
    try {
      await waitReady(origin);

      const context = await browser.newContext({
        baseURL: origin,
        viewport: { width: 400, height: 860 },
        permissions: ["notifications"],
        ignoreHTTPSErrors: true,
      });

      const endpoint = `http://127.0.0.1:${stubPort}/push/gate4-phone`;
      await context.addInitScript(
        ({ stubEndpoint, p256dh, auth }) => {
          let currentSub: {
            endpoint: string;
            expirationTime: null;
            options: { userVisibleOnly: boolean; applicationServerKey: ArrayBuffer | null };
            getKey: (name: string) => ArrayBuffer | null;
            toJSON: () => {
              endpoint: string;
              expirationTime: null;
              keys: { p256dh: string; auth: string };
            };
            unsubscribe: () => Promise<boolean>;
          } | null = null;

          function b64UrlToBuffer(b64url: string): ArrayBuffer {
            const pad = "=".repeat((4 - (b64url.length % 4)) % 4);
            const b64 = (b64url + pad).replace(/-/g, "+").replace(/_/g, "/");
            const raw = atob(b64);
            const out = new Uint8Array(raw.length);
            for (let i = 0; i < raw.length; i++) out[i] = raw.charCodeAt(i);
            return out.buffer;
          }

          function makeSub(appKey: ArrayBuffer | null) {
            return {
              endpoint: stubEndpoint,
              expirationTime: null,
              options: {
                userVisibleOnly: true,
                applicationServerKey: appKey,
              },
              getKey(name: string) {
                if (name === "p256dh") return b64UrlToBuffer(p256dh);
                if (name === "auth") return b64UrlToBuffer(auth);
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
                currentSub = null;
                return true;
              },
            };
          }

          const fakePushManager = {
            async getSubscription() {
              return currentSub;
            },
            async subscribe(options?: {
              userVisibleOnly?: boolean;
              applicationServerKey?: BufferSource | null;
            }) {
              let appKey: ArrayBuffer | null = null;
              if (options?.applicationServerKey) {
                const src = options.applicationServerKey;
                if (src instanceof ArrayBuffer) {
                  appKey = src;
                } else if (ArrayBuffer.isView(src)) {
                  appKey = src.buffer.slice(
                    src.byteOffset,
                    src.byteOffset + src.byteLength,
                  ) as ArrayBuffer;
                }
              }
              localStorage.setItem(
                "gate4.pushServerKey",
                appKey
                  ? btoa(String.fromCharCode(...new Uint8Array(appKey)))
                      .replace(/\+/g, "-")
                      .replace(/\//g, "_")
                      .replace(/=+$/, "")
                  : "",
              );
              currentSub = makeSub(appKey);
              return currentSub;
            },
            async permissionState() {
              return "granted" as const;
            },
          };

          Object.defineProperty(
            ServiceWorkerRegistration.prototype,
            "pushManager",
            {
              configurable: true,
              get() {
                return fakePushManager;
              },
            },
          );
        },
        { stubEndpoint: endpoint, p256dh: p256dhB64, auth: authB64 },
      );

      const page = await context.newPage();
      const cdp = await context.newCDPSession(page);
      await cdp.send("WebAuthn.enable", { enableUI: false });
      const { authenticatorId } = await cdp.send(
        "WebAuthn.addVirtualAuthenticator",
        {
          options: {
            protocol: "ctap2",
            transport: "internal",
            hasResidentKey: true,
            hasUserVerification: true,
            isUserVerified: true,
            automaticPresenceSimulation: true,
          },
        },
      );

      // 1. A new user signs in at 400 px.
      await page.goto(`${origin}/chat`);
      await expect(page).toHaveURL(/\/signin\?returnTo=%2Fchat/);
      await expect(page.getByRole("heading", { name: "Sign in" })).toBeVisible();
      await assertNoHorizontalOverflow(page, "/signin");

      await page.getByRole("button", { name: "Sign in with Scalekit" }).click();
      await expect(page).toHaveURL(/\/chat$/);

      const sessionInfo = await page.evaluate(async () => {
        const res = await fetch("/api/v1/auth/session", { cache: "no-store" });
        return (await res.json()) as {
          signedIn: boolean;
          authEnabled: boolean;
          user: { id: string; email: string; name: string } | null;
          role: string;
        };
      });
      expect(sessionInfo.signedIn).toBe(true);
      expect(sessionInfo.authEnabled).toBe(true);
      expect(sessionInfo.user?.email).toBe("alex.rivera@acme.test");
      expect(sessionInfo.user?.id).toMatch(/^usr_/);
      expect(sessionInfo.role).toBe("admin");
      newUserId = sessionInfo.user!.id;

      // 2. Finishes onboarding at 400 px (wizard -> scripted profiler -> persona step).
      await page.goto(`${origin}/onboarding`);
      await expect(
        page.getByRole("heading", { name: "Map the business" }),
      ).toBeVisible();
      await assertNoHorizontalOverflow(page, "/onboarding (wizard step 1)");

      await page.getByLabel("Company name").fill("Acme Outdoor");
      await page
        .getByLabel("Company operating context")
        .fill("We sell technical outdoor gear to specialty retailers and direct shoppers.");
      await page.getByRole("button", { name: "Continue" }).click();

      await expect(
        page.getByRole("heading", { name: "Set the first watchlist" }),
      ).toBeVisible();
      await assertNoHorizontalOverflow(page, "/onboarding (wizard step 2)");
      await page
        .getByLabel("Quarterly priorities")
        .fill(["Protect Q4 wholesale margin", "Prevent warehouse stock-outs"].join(NL));
      await page.getByRole("button", { name: "Build workspace" }).click();

      await expect(page).toHaveURL(/\/business-profile\?from=setup/);
      await wakeJobs();

      const continueToPersonalBtn = page.getByRole("button", {
        name: "Continue to personal setup",
      });
      await expect(continueToPersonalBtn).toBeVisible({ timeout: 15_000 });
      await expect(page.getByLabel("Business profile")).toContainText(
        "Acme Outdoor — Business Profile",
      );
      await assertNoHorizontalOverflow(page, "/business-profile");
      await continueToPersonalBtn.click();

      await expect(page).toHaveURL(/\/onboarding$/);
      await expect(
        page.getByRole("heading", { name: "Define your working context" }),
      ).toBeVisible();
      await assertNoHorizontalOverflow(page, "/onboarding (persona)");

      await page.getByLabel("Your role").fill("VP of Operations");
      await page
        .getByLabel("What needs your attention?")
        .fill(["Warehouse stock-outs", "Supplier payment holds"].join(NL));
      await page.getByRole("button", { name: "Open my workspace" }).click();

      await expect(page).toHaveURL(/\/chat$/);
      const onboardingStatus = await page.evaluate(async () => {
        const res = await fetch("/api/v1/onboarding/status", { cache: "no-store" });
        return (await res.json()) as { state: string; mode: string };
      });
      expect(onboardingStatus.state).toBe("ready");
      expect(onboardingStatus.mode).toBe("personal");

      // 3. Installs the app criteria (manifest and service worker) at 400 px.
      await assertNoHorizontalOverflow(page, "/chat");
      const manifestHref = await page
        .locator('link[rel="manifest"]')
        .getAttribute("href");
      expect(manifestHref).toBe("/manifest.webmanifest");

      const manifestRes = await fetch(`${origin}/manifest.webmanifest`);
      expect(manifestRes.status).toBe(200);
      const manifest = (await manifestRes.json()) as {
        name: string;
        short_name: string;
        start_url: string;
        scope: string;
        display: string;
        icons: Array<{ src: string; sizes: string; type: string; purpose?: string }>;
      };
      expect(manifest.display).toBe("standalone");
      expect(manifest.start_url).toBe("/chat");
      expect(manifest.scope).toBe("/");
      expect(manifest.icons.length).toBeGreaterThanOrEqual(3);
      for (const icon of manifest.icons) {
        const iconRes = await fetch(`${origin}${icon.src}`);
        expect(iconRes.status).toBe(200);
        expect(iconRes.headers.get("content-type")).toContain("image/png");
        const buf = Buffer.from(await iconRes.arrayBuffer());
        const dims = pngDimensions(buf);
        const [w, h] = icon.sizes.split("x").map(Number);
        expect(dims).toEqual({ width: w, height: h });
      }

      // Reload once if needed so the active service worker controls the page.
      const swState = await page.evaluate(async () => {
        const reg = await navigator.serviceWorker.ready;
        return {
          activeState: reg.active?.state ?? null,
          scriptURL: reg.active?.scriptURL ?? null,
          controlled: Boolean(navigator.serviceWorker.controller),
        };
      });
      expect(swState.activeState).toBe("activated");
      expect(swState.scriptURL).toBe(`${origin}/sw.js`);
      if (!swState.controlled) {
        await page.reload({ waitUntil: "networkidle" });
      }
      expect(
        await page.evaluate(() => Boolean(navigator.serviceWorker.controller)),
      ).toBe(true);

      // 4. Asks a question against the scripted model and sees the answer stream,
      //    reloads in the middle and the answer resumes.
      await page.getByRole("link", { name: "New bot" }).click();
      await expect(page).toHaveURL(/\/chat\/bots\/new$/);
      await assertNoHorizontalOverflow(page, "/chat/bots/new");
      await page.getByLabel("Name").fill("Ops Scout");
      await page.getByRole("button", { name: "Create" }).click();
      await expect(page).toHaveURL(/\/chat\/t\/[0-9a-f-]{36}$/);
      const workThreadUrl = page.url();
      await assertNoHorizontalOverflow(page, "/chat/t/:threadId");

      const composer = page.locator("textarea.work-input");
      await expect(composer).toBeVisible();
      await composer.fill("How is Q4 warehouse stock trending?");
      await composer.press("Enter");

      await firstChunkSent;
      await expect(
        page.locator(".work-transcript"),
      ).toContainText("First half of the scripted answer is streaming live.", {
        timeout: 15_000,
      });
      await assertNoHorizontalOverflow(page, "/chat/t/:threadId (streaming)");

      // Reload in the middle of the in-flight stream, then release the second half.
      await page.reload({ waitUntil: "domcontentloaded" });
      await expect(
        page.locator(".work-transcript"),
      ).toContainText("First half of the scripted answer is streaming live.", {
        timeout: 15_000,
      });
      releaseSecondChunk();

      await expect(page.locator(".work-transcript")).toContainText(
        fullStreamedAnswer,
        {
          timeout: 15_000,
        },
      );
      await assertNoHorizontalOverflow(page, "/chat/t/:threadId (resumed)");

      // 5. Turns on Web Push against the local push stub at 400 px.
      await page.goto(`${origin}/profile`, { waitUntil: "networkidle" });
      await assertNoHorizontalOverflow(page, "/profile");

      await expect(
        page.getByRole("heading", { name: "Push notifications", exact: true }),
      ).toBeVisible();
      await expect(page.getByTestId("push-status")).toHaveText(
        "Notifications are off in this browser.",
      );
      await page
        .getByRole("button", { name: "Turn on notifications", exact: true })
        .click();
      await expect(page.getByTestId("push-status")).toHaveText(
        "Notifications are on in this browser.",
      );
      await expect(
        page.getByRole("button", { name: "Turn off notifications", exact: true }),
      ).toBeVisible();

      const subCount = await client.query<{ n: string }>(
        `select count(*)::text as n from app_push_subscription where org_id = $1 and user_id = $2`,
        [orgId, newUserId],
      );
      expect(Number(subCount.rows[0]?.n ?? "0")).toBe(1);

      // 6. Registers a passkey with the virtual authenticator at 400 px.
      await expect(
        page.getByRole("heading", { name: "Passkeys", exact: true }),
      ).toBeVisible();
      await page.getByLabel("Passkey name").fill("Gate 4 Phone");
      await page
        .getByRole("button", { name: "Add passkey", exact: true })
        .click();

      await expect(page.getByText("Gate 4 Phone")).toBeVisible();
      await expect(page.getByText("Registered")).toBeVisible();
      await assertNoHorizontalOverflow(page, "/profile (with push + passkey)");

      const pkRes = await client.query<{ credential_id: string }>(
        `select credential_id from app_passkey where org_id = $1 and user_id = $2`,
        [orgId, newUserId],
      );
      expect(pkRes.rows).toHaveLength(1);
      const registeredCredentialId = pkRes.rows[0]!.credential_id;

      // 7. Gets an action request from the scripted model and receives the push notice
      //    (with no business data in it).
      await page.goto(workThreadUrl, { waitUntil: "networkidle" });
      await expect(composer).toBeVisible();
      await expect(composer).toBeEnabled();
      await composer.fill("Hold the disputed supplier wire for SKU-999");
      await composer.press("Enter");

      await expect(
        page.locator(".work-transcript"),
      ).toContainText("I have queued the supplier wire hold for your approval.", {
        timeout: 15_000,
      });

      await expect
        .poll(
          async () => {
            const r = await client.query<{ id: string; status: string }>(
              `select id::text, status from action_request
                where org_id = $1 and kind = 'gate4.supplier_hold'
                order by created_at desc limit 1`,
              [orgId],
            );
            return r.rows[0] ?? { srvLogsTail: srvLogs.slice(-600) };
          },
          { timeout: 10_000 },
        )
        .toMatchObject({ status: "pending_approval" });

      const arRes = await client.query<{ id: string; status: string }>(
        `select id::text, status from action_request
          where org_id = $1 and kind = 'gate4.supplier_hold'
          order by created_at desc limit 1`,
        [orgId],
      );
      expect(arRes.rows).toHaveLength(1);
      const actionRequestId = arRes.rows[0]!.id;
      expect(arRes.rows[0]!.status).toBe("pending_approval");

      // Wake River to deliver the queued approval_waiting Web Push notification.
      await wakeJobs();

      await expect
        .poll(
          () => receivedPushes.find((p) => p.notice.event === "approval_waiting"),
          { timeout: 5_000 },
        )
        .toBeDefined();
      const approvalPush = receivedPushes.find(
        (p) => p.notice.event === "approval_waiting",
      )!;
      await expect
        .poll(
          async () => {
            const n = receivedPushes.filter((p) => p.notice.event === "run_finished").length;
            if (n === 0) await wakeJobs();
            return n;
          },
          { timeout: 15_000 },
        )
        .toBe(1);
      expect(receivedPushes.find((p) => p.notice.event === "run_finished")!.notice).toEqual({
        title: "OpenNeko",
        body: "A run finished.",
        url: "/runs",
        event: "run_finished",
      });
      expect(approvalPush.headers["content-encoding"]).toBe("aes128gcm");
      const subscribedKey = await page.evaluate(
        () => localStorage.getItem("gate4.pushServerKey") ?? "",
      );
      const serverKey = await page.evaluate(async () => {
        const r = await fetch("/api/v1/my/push-subscriptions");
        return ((await r.json()) as { vapidPublicKey?: string }).vapidPublicKey ?? "";
      });
      expect(serverKey).toMatch(/^B[A-Za-z0-9_-]{86}$/);
      expect(subscribedKey).toBe(serverKey);
      verifyVapidForSubscription(
        String(approvalPush.headers["authorization"] ?? ""),
        subscribedKey,
        `http://127.0.0.1:${stubPort}`,
      );
      expect(approvalPush.notice).toEqual({
        title: "OpenNeko",
        body: "An approval is waiting.",
        url: "/actions",
        event: "approval_waiting",
      });
      expect(Object.keys(approvalPush.notice).sort()).toEqual([
        "body",
        "event",
        "title",
        "url",
      ]);
      for (const secret of [
        "VENDOR-SECRET-99",
        "48,500",
        "48500",
        "SKU-999",
        "Acme Industrial Supplies",
        "hold_payment",
        actionRequestId,
      ]) {
        expect(approvalPush.rawJson).not.toContain(secret);
      }

      // Deliver the decrypted push notice into the active Service Worker (`sw.js`)
      // and verify it posts OPENNEKO_PUSH_NOTICE with url "/actions".
      const swWorker =
        context.serviceWorkers()[0] ?? (await context.waitForEvent("serviceworker"));
      const swNoticePromise = page.evaluate(
        () =>
          new Promise<{
            type: string;
            title: string;
            body: string;
            url: string;
            event: string;
          }>((resolve, reject) => {
            const timer = window.setTimeout(
              () => reject(new Error("timed out waiting for SW push message")),
              5000,
            );
            const onMsg = (ev: MessageEvent) => {
              if (ev.data?.type === "OPENNEKO_PUSH_NOTICE") {
                window.clearTimeout(timer);
                navigator.serviceWorker.removeEventListener("message", onMsg);
                resolve(ev.data);
              }
            };
            navigator.serviceWorker.addEventListener("message", onMsg);
          }),
      );
      await swWorker.evaluate((rawJson) => {
        const swGlobal = globalThis as unknown as {
          PushEvent: new (type: string, init: { data: string }) => Event;
          dispatchEvent: (event: Event) => boolean;
        };
        swGlobal.dispatchEvent(new swGlobal.PushEvent("push", { data: rawJson }));
      }, approvalPush.rawJson);
      const swNotice = await swNoticePromise;
      expect(swNotice).toEqual({
        type: "OPENNEKO_PUSH_NOTICE",
        title: "OpenNeko",
        body: "An approval is waiting.",
        url: "/actions",
        event: "approval_waiting",
      });

      // 8. Approves with the passkey at 400 px, and the audit log holds the
      //    approval with method `passkey`.
      const shown = await swWorker.evaluate(async () => {
        const reg = (globalThis as unknown as { registration: ServiceWorkerRegistration }).registration;
        return (await reg.getNotifications({ tag: "approval_waiting" })).map((n) => ({
          title: n.title,
          body: n.body,
          data: n.data as unknown,
        }));
      });
      expect(shown).toEqual([
        { title: "OpenNeko", body: "An approval is waiting.", data: { event: "approval_waiting", url: "/actions" } },
      ]);
      await swWorker.evaluate(async () => {
        const g = globalThis as unknown as {
          registration: ServiceWorkerRegistration;
          NotificationEvent: new (type: string, init: { notification: Notification }) => Event;
          dispatchEvent: (event: Event) => boolean;
        };
        const [notice] = await g.registration.getNotifications({ tag: "approval_waiting" });
        g.dispatchEvent(new g.NotificationEvent("notificationclick", { notification: notice! }));
      });
      await page.waitForURL(`${origin}${swNotice.url}`);
      await page.waitForLoadState("networkidle");
      await assertNoHorizontalOverflow(page, "/actions");
      await page.goto(`${origin}/actions/${actionRequestId}`, {
        waitUntil: "networkidle",
      });
      await expect(
        page.getByRole("heading", {
          name: "Hold $48,500 wire to Acme Industrial Supplies for SKU-999",
        }),
      ).toBeVisible();
      await assertNoHorizontalOverflow(page, "/actions/:id");

      await page
        .getByRole("button", { name: "Approve with passkey", exact: true })
        .click();

      await expect
        .poll(async () => {
          const r = await client.query<{ status: string }>(
            `select status from action_request where id = $1`,
            [actionRequestId],
          );
          return r.rows[0]?.status;
        })
        .toBe("approved");
      await assertNoHorizontalOverflow(page, "/actions/:id (approved)");

      const approvedRow = await client.query<{
        status: string;
        approved_by_user_id: string | null;
      }>(
        `select status, approved_by_user_id from action_request where id = $1`,
        [actionRequestId],
      );
      expect(approvedRow.rows[0]?.status).toBe("approved");
      expect(approvedRow.rows[0]?.approved_by_user_id).toBe(newUserId);

      const auditRes = await client.query<{
        event: string;
        payload: {
          method?: string;
          passkeyCredentialId?: string;
          passkeyName?: string;
          approvedBy?: string;
          kind?: string;
          userVerified?: boolean;
        };
      }>(
        `select event, payload from audit_chain
          where org_id = $1 and entity_kind = 'action_request' and entity_id = $2 and event = 'approved'
          order by seq desc limit 1`,
        [orgId, actionRequestId],
      );
      expect(auditRes.rows).toHaveLength(1);
      expect(auditRes.rows[0]!.payload).toMatchObject({
        method: "passkey",
        passkeyCredentialId: registeredCredentialId,
        passkeyName: "Gate 4 Phone",
        approvedBy: newUserId,
        kind: "gate4.supplier_hold",
        userVerified: true,
      });

      // 9. An approval with no passkey gets 403.
      const runInfoRes = await client.query<{
        work_run_id: string;
        thread_id: string;
      }>(
        `select ar.work_run_id::text as work_run_id, wr.thread_id::text as thread_id
           from action_request ar
           join work_run wr on wr.id = ar.work_run_id
          where ar.id = $1`,
        [actionRequestId],
      );
      const { work_run_id: workRunId, thread_id: threadId } = runInfoRes.rows[0]!;

      await client.query(
        `insert into action_request
           (id, org_id, actor_user_id, actor_role, actor_backend, work_run_id, scope, kind, target, payload, risk_level, status, summary, intent)
         values ($1, $2, $3, 'admin', 'ax', $4, 'external', 'gate4.supplier_hold', 'VENDOR-SECRET-100', '{"operation":"hold_payment"}'::jsonb, 'low', 'pending_approval', 'Second hold requiring passkey', 'Verify 403 without passkey')`,
        [SECOND_AR_ID, orgId, newUserId, workRunId],
      );

      const plainPatchApprove = await page.evaluate(async (id) => {
        const res = await fetch(`/api/v1/action-requests/${id}`, {
          method: "PATCH",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ decision: "approve" }),
        });
        return {
          status: res.status,
          body: (await res.json()) as { code?: string; error?: string },
        };
      }, SECOND_AR_ID);
      expect(plainPatchApprove.status).toBe(403);
      expect(plainPatchApprove.body).toEqual({
        code: "passkey_required",
        error: "This approval needs your passkey.",
      });

      const plainChatApprove = await page.evaluate(
        async ({ tId, rId, arId }) => {
          const res = await fetch(
            `/api/v1/work/threads/${tId}/runs/${rId}/approve-action`,
            {
              method: "POST",
              headers: { "Content-Type": "application/json" },
              body: JSON.stringify({
                actionRequestId: arId,
                decision: "approve",
              }),
            },
          );
          return {
            status: res.status,
            body: (await res.json()) as { code?: string; error?: string },
          };
        },
        { tId: threadId, rId: workRunId, arId: SECOND_AR_ID },
      );
      expect(plainChatApprove.status).toBe(403);
      expect(plainChatApprove.body).toEqual({
        code: "passkey_required",
        error: "This approval needs your passkey.",
      });

      const stillPending = await client.query<{ status: string }>(
        `select status from action_request where id = $1`,
        [SECOND_AR_ID],
      );
      expect(stillPending.rows[0]?.status).toBe("pending_approval");

      await cdp.send("WebAuthn.removeVirtualAuthenticator", {
        authenticatorId,
      });
      await context.close();
    } finally {
      srv.kill("SIGTERM");
      serveSocketServer.close();
      httpStub.close();
      scalekitStub.close();
      await client
        .query(`delete from customer_profile where org_id = $1 and version > 1`, [
          orgId,
        ])
        .catch(() => {});
      await client
        .query(
          `update customer_profile set is_current = true where org_id = $1 and version = 1`,
          [orgId],
        )
        .catch(() => {});
      if (newUserId) {
        await client
          .query(`delete from app_push_subscription where user_id = $1`, [
            newUserId,
          ])
          .catch(() => {});
        await client
          .query(`delete from app_passkey where user_id = $1`, [newUserId])
          .catch(() => {});
      }
      await client
        .query(`delete from action_request where kind = 'gate4.supplier_hold'`)
        .catch(() => {});
      await client
        .query(`delete from pack_artifact where id = $1`, [PACK_ARTIFACT_ID])
        .catch(() => {});
      await client
        .query(`delete from pack_action_definition where id = $1`, [
          PACK_ACT_DEF_ID,
        ])
        .catch(() => {});
      await client
        .query(`delete from action_policy where id = $1`, [POLICY_ID])
        .catch(() => {});
      await client
        .query(`delete from data_source where id = $1`, [DATA_SOURCE_ID])
        .catch(() => {});
      await client
        .query(`delete from llm_provider_config where org_id = $1`, [orgId])
        .catch(() => {});
      await client
        .query(`delete from idp_group_rule where id = $1`, [IDP_RULE_ID])
        .catch(() => {});
      await client
        .query(`delete from sso_group where id = $1`, [SSO_GROUP_ID])
        .catch(() => {});
      if (newUserId) {
        await client
          .query(
            `delete from work_thread where created_by_user_id = $1`,
            [newUserId],
          )
          .catch(() => {});
        await client
          .query(`delete from operator_profile where user_id = $1`, [
            newUserId,
          ])
          .catch(() => {});
        await client
          .query(`delete from app_user where id = $1`, [newUserId])
          .catch(() => {});
      }
      await client.end();
      rmSync(work, { recursive: true, force: true });
    }
  });
});
