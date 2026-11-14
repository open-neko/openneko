import { readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import type { InteractionEvent } from "@neko/interaction";
import { deliverHud } from "../src/hud/channel";
import { deliverSlack } from "../src/slack/channel";
import { deliverTelegram } from "../src/telegram/channel";
import type { ChannelEnv, ChannelRecipient, DeliverResult } from "../src/channel-adapter";

// The Go channel sends (apps/openneko/internal/webfront/channel*.go) replay
// these requests. Run with UPDATE_PROMPT_FIXTURES=1 to rewrite the fixture.
const FIXTURE = join(__dirname, "../../../apps/openneko/internal/webfront/testdata/channeldeliver.json");

const MARKDOWN = [
  "# Stock report",
  "Revenue rose to **$4.7M** in *Q3*; see `orders_daily` and [the board](https://example.com/b?x=1&y=2).",
  "",
  "",
  "",
  "- first_item with snake_case",
  "2) second __bold__ ~~old~~",
  "> quoted <tag> & more",
  "> second quote line",
  "---",
  "```sql",
  "select * from t where a < 3",
  "```",
  "```",
  "plain fence",
  "```",
  "***",
  "Done. Next line",
].join("\n");

const SURFACES = [
  { version: "v1.0", createSurface: { surfaceId: "s", catalogId: "urn:app:catalog:answer:v1" } },
  {
    version: "v1.0",
    updateComponents: {
      surfaceId: "s",
      components: [
        {
          id: "root",
          component: "Answer",
          eyebrow: "Daily <stock>",
          title: "Three SKUs are low",
          subtitle: "As of 04 September",
          children: [
            "md",
            { id: "card", component: "MetricCard", metric: "$18,400", label: "At risk", mood: "act", detail: "## Detail\n- one\n```\nx\n```" },
            "table",
            "callout",
            "section",
            "missing",
            "choice",
            "divider",
            "plain-divider",
            "confirm",
          ],
        },
        { id: "md", component: "Markdown", text: "Two **suppliers** are late." },
        {
          id: "table",
          component: "Table",
          caption: "Low stock",
          columns: [
            { key: "sku", label: "SKU" },
            { key: "qty", label: "Qty", align: "right" },
            { key: "note", label: "Note" },
          ],
          rows: [
            { sku: "A-1", qty: 3, note: "ok 🚚" },
            { sku: "LONG-SKU-22", qty: 120 },
          ],
        },
        { id: "callout", component: "Callout", mood: "watch", title: "Watch", text: "> late `ETA`\n# Head" },
        { id: "section", component: "Section", title: "More", children: [{ id: "sec-md", component: "Markdown", text: "Inner text" }] },
        { id: "choice", component: "Choice", options: [{ label: "Show suppliers" }, { prompt: "Reorder now" }, {}] },
        { id: "divider", component: "Divider", label: "Then" },
        { id: "plain-divider", component: "Divider" },
        { id: "confirm", component: "Confirmation", label: "Saved", title: "Watcher <on>", children: [] },
      ],
    },
  },
];

const FLAT_SURFACES = [
  {
    updateComponents: {
      components: [
        { id: "a", component: "Markdown", text: "First" },
        { id: "b", component: "Callout", text: "Second" },
        { id: "c", component: "Unknown" },
      ],
    },
  },
];

const ROOT_NO_CHILDREN = [
  {
    updateComponents: {
      components: [
        { id: "root", component: "Briefing", greeting: "Good morning" },
        { id: "x", component: "MetricCard", metric: "12", label: "Orders" },
      ],
    },
  },
];

const LONG = Array.from({ length: 90 }, (_, i) => `Paragraph ${i} has **bold words** and _plain_ text that runs on for a while to fill space.`).join("\n\n");
const LONG_TABLE = `\`\`\`\n${Array.from({ length: 160 }, (_, i) => `row ${i} ${"x".repeat(20)}`).join("\n")}\n\`\`\``;

const ev = (events: unknown[]): InteractionEvent[] => events as InteractionEvent[];

const EVENT_SETS: Record<string, InteractionEvent[]> = {
  markdown: ev([{ kind: "converse", id: "c1", role: "assistant", text: MARKDOWN }]),
  surfaces: ev([{ kind: "converse", id: "c2", role: "assistant", text: "ignored", enrichment: { surfaces: SURFACES } }]),
  flat: ev([{ kind: "inform", id: "i0", mood: "good", title: "t", body: "b", enrichment: { surfaces: FLAT_SURFACES } }]),
  rootless: ev([{ kind: "converse", id: "c3", role: "assistant", text: "", enrichment: { surfaces: ROOT_NO_CHILDREN } }]),
  inform: ev([
    {
      kind: "inform",
      id: "o1",
      mood: "act",
      title: "Stock <low>",
      body: "Three SKUs are low. Reorder today! More text.\n\nSecond paragraph.",
      metric: { label: "SKUs", value: "3" },
      series: [{ t: "a", v: 1 }],
      evidence: [{ ref: "sku:A-1" }],
    },
    { kind: "inform", id: "o2", mood: "unknown", title: "Plain", body: "" },
  ]),
  asks: ev([
    { kind: "ask", id: "a1", ask: "approval", prompt: "Approve the **refund**?", decisionRef: "req-1", risk: "high" },
    {
      kind: "ask",
      id: "a2",
      ask: "choice",
      prompt: "Pick one",
      decisionRef: "req-2",
      options: Array.from({ length: 6 }, (_, i) => ({ id: `o${i}`, label: i === 0 ? "L".repeat(70) : `Option ${i}` })),
    },
  ]),
  choiceOnly: ev([
    { kind: "ask", id: "a3", ask: "choice", prompt: "Pick", decisionRef: "req-3", options: [{ id: "x", label: "X" }] },
    { kind: "ask", id: "a4", ask: "free", prompt: "Say more", decisionRef: "req-4" },
  ]),
  misc: ev([
    { kind: "resolve", id: "r1", ref: "req-1", status: "succeeded", summary: "Refund <sent>" },
    { kind: "resolve", id: "r2", ref: "req-2", status: "rejected", summary: "No" },
    { kind: "resolve", id: "r3", ref: "req-3", status: "failed", summary: "Broke" },
    { kind: "highlight", id: "h1", metrics: [{ label: "Orders", value: "49,310", sub: "+4%" }, { label: "AOV", value: "$75" }] },
    { kind: "offer", id: "f1", label: "Report <pdf>", artifactRef: "https://example.com/r.pdf?a=1&b=2", mime: "application/pdf" },
    { kind: "offer", id: "f2", label: "Local", artifactRef: "files/r.csv" },
    { kind: "progress", id: "p1", phase: "start", label: "Reading orders" },
  ]),
  followupsWithApproval: ev([
    { kind: "converse", id: "c4", role: "assistant", text: "x", enrichment: { surfaces: SURFACES } },
    { kind: "ask", id: "a5", ask: "approval", prompt: "Go?", decisionRef: "req-5" },
  ]),
  followupsOnly: ev([
    { kind: "converse", id: "c5", role: "assistant", text: "x", enrichment: { surfaces: [{ updateComponents: { components: [{ id: "root", component: "Answer", children: ["ch"] }, { id: "ch", component: "Choice", options: [{ label: "Next" }] }] } }] } },
  ]),
  long: ev([{ kind: "converse", id: "c6", role: "assistant", text: LONG }]),
  longTable: ev([{ kind: "converse", id: "c7", role: "assistant", text: `Intro **bold** line\n\n${LONG_TABLE}\n\nAfter` }]),
  spatial: ev([
    { kind: "inform", id: "s1", mood: "watch", title: "Here", body: "b", spatial: { kind: "site", id: "plant-2", longitude: "12.5", latitude: 41.9 } },
    { kind: "inform", id: "s2", mood: "watch", title: "There", body: "b", enrichment: { spatial: { longitude: 1, latitude: 2 } } },
    { kind: "converse", id: "s3", role: "assistant", text: "Hello" },
  ]),
  empty: [],
};

interface Case {
  name: string;
  channel: "slack" | "telegram" | "hud";
  recipient: ChannelRecipient;
  env: ChannelEnv;
  events: string;
}

const CASES: Case[] = [];
for (const events of Object.keys(EVENT_SETS)) {
  CASES.push({ name: `slack ${events}`, channel: "slack", recipient: { kind: "slack", channel: "D1", thread_ts: "1700.1" }, env: { SLACK_BOT_TOKEN: "xoxb-1" }, events });
  CASES.push({ name: `telegram ${events}`, channel: "telegram", recipient: { kind: "telegram", chatId: 7 }, env: { TELEGRAM_BOT_TOKEN: "123:abc" }, events });
  CASES.push({ name: `hud ${events}`, channel: "hud", recipient: { kind: "hud", workspace: "plant" }, env: { HUD_CHANNEL_SECRET: "s3cret", HUD_CHANNEL_WORKSPACE: " pack " }, events });
}
CASES.push({ name: "slack ephemeral", channel: "slack", recipient: { kind: "slack", channel: "C9", user: "U1", ephemeral: true }, env: { SLACK_BOT_TOKEN: "xoxb-1" }, events: "misc" });
CASES.push({ name: "telegram string chat", channel: "telegram", recipient: { kind: "telegram", chatId: "-100" }, env: { TELEGRAM_BOT_TOKEN: "123:abc" }, events: "inform" });
CASES.push({ name: "hud default anchor", channel: "hud", recipient: { kind: "hud", defaultAnchor: "line:4" }, env: { HUD_CHANNEL_SECRET: "s3cret" }, events: "spatial" });
CASES.push({ name: "hud plain default anchor", channel: "hud", recipient: { kind: "hud", defaultAnchor: "yard" }, env: { HUD_CHANNEL_SECRET: "s3cret" }, events: "misc" });

interface Request {
  path: string;
  headers?: Record<string, string>;
  body: string;
}

async function run(c: Case): Promise<{ requests: Request[]; result: DeliverResult }> {
  const requests: Request[] = [];
  const events = EVENT_SETS[c.events]!;
  let n = 0;
  if (c.channel === "slack") {
    const result = await deliverSlack(
      { recipient: c.recipient, events, env: c.env },
      {
        createClient: () => ({
          postJson: async (method, body) => {
            requests.push({ path: `/${method}`, body: JSON.stringify(body) });
            return { ok: true, ts: "1700.9" };
          },
          postForm: async () => ({ ok: true }),
          get: async () => ({ ok: true }),
        }),
      },
    );
    return { requests, result };
  }
  if (c.channel === "telegram") {
    const result = await deliverTelegram(
      { recipient: c.recipient, events, env: c.env },
      {
        createClient: (token) => ({
          call: async <T>(method: string, body: Record<string, unknown>) => {
            requests.push({ path: `/bot${token}/${method}`, body: JSON.stringify(body) });
            n += 1;
            return { message_id: 100 + n } as T;
          },
        }),
      },
    );
    return { requests, result };
  }
  const result = await deliverHud(
    { recipient: c.recipient, events, env: c.env },
    {
      now: () => new Date("2026-11-14T10:00:00.000Z"),
      randomId: () => "delivery-1",
      channelUrl: "",
      fetch: (async (url: string, init: RequestInit) => {
        const headers = init.headers as Record<string, string>;
        requests.push({
          path: url,
          headers: {
            "x-openneko-hud-delivery-id": headers["x-openneko-hud-delivery-id"]!,
            "x-openneko-hud-signature": headers["x-openneko-hud-signature"]!,
          },
          body: String(init.body),
        });
        return new Response("ok", { status: 200 });
      }) as unknown as typeof fetch,
    },
  );
  return { requests, result };
}

describe("Go channel delivery fixtures", () => {
  it("matches the stored fixture", async () => {
    const cases = [];
    for (const c of CASES) cases.push({ ...c, ...(await run(c)) });
    const fixture = `${JSON.stringify({ events: EVENT_SETS, cases }, null, 2)}\n`;
    if (process.env.UPDATE_PROMPT_FIXTURES === "1") writeFileSync(FIXTURE, fixture);
    expect(fixture).toBe(readFileSync(FIXTURE, "utf8"));
  });
});
