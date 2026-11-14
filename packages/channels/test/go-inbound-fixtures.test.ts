import { readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { parseHudUpdate, verifyHudRequest } from "../src/hud/channel";
import { signBody } from "../src/hud/security";
import { parseSlackUpdate, verifySlackRequest } from "../src/slack/channel";
import { verifySlackSignature } from "../src/slack/verify";
import { parseTelegramUpdate, verifyTelegramRequest } from "../src/telegram/channel";
import { createHmac } from "node:crypto";

// The Go inbound parsers (apps/openneko/internal/webfront/channelinbound.go)
// replay these cases. Run with UPDATE_PROMPT_FIXTURES=1 to rewrite the fixture.
const FIXTURE = join(__dirname, "../../../apps/openneko/internal/webfront/testdata/channelinbound.json");

const APPROVE_ID = "3f1c2b9a-0d4e-4c55-8a71-2b3c4d5e6f70";

const slack: unknown[] = [
  { command: "/openneko", text: "  report weekly  sales ", channel_id: "C1", user_id: "U1", user_name: "ada", team_id: "T1" },
  { command: "/status", text: "", channel_id: "C1" },
  { command: "/", text: "   " , channel_id: "", user_id: "U2" },
  {
    type: "block_actions",
    user: { id: "U3", username: "grace" },
    team: { id: "T2" },
    channel: { id: "C2" },
    message: { ts: "1700.1" },
    actions: [{ action_id: "approve", value: APPROVE_ID }, { action_id: "noop" }, "bad"],
  },
  {
    type: "block_actions",
    user: { id: "U3", name: "" , username: "grace" },
    container: { channel_id: "C3", message_ts: "1700.2" },
    actions: [{ action_id: "reject", value: 7 }, { action_id: "select:opt-1", text: { text: "Weekly" } }, { action_id: "select:opt-2" }],
  },
  { type: "block_actions", actions: "none" },
  {
    type: "event_callback",
    team_id: "T1",
    event: { type: "app_mention", text: " <@U0BOT>  what sold  ", user: "U4", channel: "C4", ts: "1700.3" },
  },
  { type: "event_callback", event: { type: "app_mention", text: "<@U0BOT>", user: "U4", channel: "C4", ts: "1700.4", thread_ts: "1700.0" } },
  { type: "event_callback", event: { type: "message", channel_type: "im", text: "hello", user: "U5", channel: "D1", thread_ts: "1700.5", team: "T9" } },
  { type: "event_callback", event: { type: "message", channel_type: "im", text: "hi", user: "U5", channel: "D1" } },
  { type: "event_callback", event: { type: "message", channel_type: "channel", text: "more", user: "U6", channel: "C5", thread_ts: "1700.6" } },
  { type: "event_callback", event: { type: "message", channel_type: "channel", text: "chatter", user: "U6", channel: "C5" } },
  { type: "event_callback", event: { type: "message", channel_type: "im", text: "beep", bot_id: "B1", channel: "D2" } },
  { type: "event_callback", event: { type: "message", channel_type: "im", text: "edit", subtype: "message_changed", user: "U5", channel: "D1" } },
  { type: "event_callback", event: { type: "message", channel_type: "channel", text: "x", user: "U6", channel: "C5", thread_ts: "" } },
  { type: "url_verification", challenge: "abc" },
  "not an object",
  null,
];

const telegram: unknown[] = [
  { update_id: 10, message: { message_id: 1, chat: { id: 4242, type: "private" }, from: { id: 99, first_name: "Ada", last_name: "Lovelace" }, text: "What sold today?" } },
  { update_id: 11, message: { chat: { id: -1001234567890 }, from: { id: 99, username: "ada" }, text: "/report  weekly sales  " } },
  { update_id: 12, message: { chat: { id: 4242 }, from: { id: 99 }, text: "/start" } },
  { update_id: 13, message: { chat: { id: 4242 }, from: { id: 99 }, text: "/ hi" } },
  { update_id: 14, callback_query: { id: "cq", data: `approve:${APPROVE_ID}`, from: { id: 98, first_name: "Grace" }, message: { chat: { id: 4242 } } } },
  { update_id: 15, callback_query: { data: "select:pick:opt-2", from: { id: 98 }, message: { chat: { id: "chat-7" } } } },
  { update_id: 16, callback_query: { data: "noop:x", from: { id: 98 }, message: { chat: { id: 4242 } } } },
  { update_id: 17, callback_query: { data: "", from: { id: 98 } } },
  { update_id: 18, channel_post: { chat: { id: -100 }, text: "posted" } },
  { update_id: 19, edited_message: { chat: { id: 4242 }, from: { id: "99" }, text: "edited" } },
  { update_id: 20, message: { chat: { id: 4242 }, from: { id: 99 }, photo: [] } },
  { ok: true, result: [{ message: { chat: { id: 1 }, text: "a" } }, { callback_query: { data: "reject:r1" } }, 5] },
  [{ message: { chat: { id: 2 }, text: "b" } }],
  "text",
];

const hud: Array<{ raw: unknown; env: Record<string, string> }> = [
  { raw: { kind: "decision", decisionRef: APPROVE_ID, choice: "approve", reason: "fine", operatorId: "op-1", operatorName: "Ops" }, env: { HUD_CHANNEL_WORKSPACE: " plant-7 " } },
  { raw: { kind: "decision", decisionRef: APPROVE_ID, choice: "reject" }, env: {} },
  { raw: { kind: "utterance", text: "  status of line 3  " }, env: {} },
  { raw: { kind: "utterance", text: "   " }, env: {} },
  { raw: { kind: "decision", choice: "approve" }, env: {} },
  { raw: { kind: "decision", decisionRef: "x", choice: "maybe" }, env: {} },
  { raw: { kind: "wave" }, env: {} },
  { raw: [], env: {} },
];

function parsed(fn: () => unknown): unknown {
  try {
    return { parsed: JSON.parse(JSON.stringify(fn())) };
  } catch (err) {
    return { error: (err as Error).message };
  }
}

const NOW = 1_763_100_000;
const slackBody = '{"type":"event_callback","event":{"type":"message"}}';
const slackSig = (ts: string, secret = "signing-secret") =>
  `v0=${createHmac("sha256", secret).update(`v0:${ts}:${slackBody}`).digest("hex")}`;
const hudBody = '{"kind":"utterance","text":"hi"}';

function verifyCases() {
  const cases: Array<{ channel: string; env: Record<string, string>; headers: Record<string, string>; body: string; now?: number }> = [
    { channel: "slack", env: { SLACK_SIGNING_SECRET: "signing-secret" }, headers: { "X-Slack-Request-Timestamp": String(NOW - 10), "X-Slack-Signature": slackSig(String(NOW - 10)) }, body: slackBody, now: NOW },
    { channel: "slack", env: { SLACK_SIGNING_SECRET: "signing-secret" }, headers: { "x-slack-request-timestamp": String(NOW - 301), "x-slack-signature": slackSig(String(NOW - 301)) }, body: slackBody, now: NOW },
    { channel: "slack", env: { SLACK_SIGNING_SECRET: "signing-secret" }, headers: { "x-slack-request-timestamp": String(NOW), "x-slack-signature": slackSig(String(NOW), "other") }, body: slackBody, now: NOW },
    { channel: "slack", env: { SLACK_SIGNING_SECRET: "signing-secret" }, headers: { "x-slack-request-timestamp": "soon", "x-slack-signature": slackSig("soon") }, body: slackBody, now: NOW },
    { channel: "slack", env: {}, headers: { "x-slack-request-timestamp": String(NOW), "x-slack-signature": slackSig(String(NOW)) }, body: slackBody, now: NOW },
    { channel: "telegram", env: { TELEGRAM_WEBHOOK_SECRET: "tg-secret" }, headers: { "X-Telegram-Bot-Api-Secret-Token": "tg-secret" }, body: "{}" },
    { channel: "telegram", env: { TELEGRAM_WEBHOOK_SECRET: "tg-secret" }, headers: { "x-telegram-bot-api-secret-token": "tg-secreT" }, body: "{}" },
    { channel: "telegram", env: {}, headers: { "x-telegram-bot-api-secret-token": "" }, body: "{}" },
    { channel: "hud", env: { HUD_CHANNEL_SECRET: "hud-secret" }, headers: { "x-openneko-hud-signature": `sha256=${signBody(hudBody, "hud-secret")}` }, body: hudBody },
    { channel: "hud", env: { HUD_CHANNEL_SECRET: " hud-secret " }, headers: { "x-openneko-hud-signature": signBody(hudBody, "hud-secret").toUpperCase() }, body: hudBody },
    { channel: "hud", env: { HUD_CHANNEL_SECRET: "hud-secret" }, headers: { "x-openneko-hud-signature": `sha256=${"0".repeat(64)}` }, body: hudBody },
    { channel: "hud", env: { HUD_CHANNEL_SECRET: "hud-secret" }, headers: { "x-openneko-hud-signature": "sha256=zz" }, body: hudBody },
    { channel: "hud", env: {}, headers: { "x-openneko-hud-signature": `sha256=${signBody(hudBody, "hud-secret")}` }, body: hudBody },
  ];
  return cases.map((c) => {
    let ok: boolean;
    if (c.channel === "slack") {
      ok = c.env.SLACK_SIGNING_SECRET ? verifySlackSignature(c.env.SLACK_SIGNING_SECRET, c.headers, c.body, c.now) : verifySlackRequest(c.headers, c.body, c.env);
    } else if (c.channel === "telegram") {
      ok = verifyTelegramRequest(c.headers, c.body, c.env);
    } else {
      ok = verifyHudRequest(c.headers, c.body, c.env);
    }
    return { ...c, ok };
  });
}

describe("Go inbound fixtures", () => {
  it("match the Node parsers and checks", () => {
    const fixture = {
      parse: [
        ...slack.map((raw) => ({ channel: "slack", raw, env: {}, ...(parsed(() => parseSlackUpdate(raw)) as object) })),
        ...telegram.map((raw) => ({ channel: "telegram", raw, env: {}, ...(parsed(() => parseTelegramUpdate(raw)) as object) })),
        ...hud.map(({ raw, env }) => ({ channel: "hud", raw, env, ...(parsed(() => parseHudUpdate(raw, env)) as object) })),
      ],
      verify: verifyCases(),
    };
    const text = `${JSON.stringify(fixture, null, 2)}\n`;
    if (process.env.UPDATE_PROMPT_FIXTURES === "1") writeFileSync(FIXTURE, text);
    expect(readFileSync(FIXTURE, "utf8")).toBe(text);
  });
});
