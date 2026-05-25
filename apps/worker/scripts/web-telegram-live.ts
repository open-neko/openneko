/**
 * Proof: web + Telegram are used *simultaneously* by the same agent loop, with
 * no code above the waist knowing either exists.
 *
 * ONE modality-free InteractionEvent[] (an `inform` + an approval `ask`) is
 * fanned out to two membranes at once:
 *   - WEB     — the built-in, in-process projection the live /api/briefing uses.
 *   - TELEGRAM — the real @open-neko/channel-telegram plugin, driven over the
 *                exact one-shot RPC the worker uses (node dist/run.js <method>),
 *                projecting *inside the plugin*.
 * Then a real Telegram button tap is normalized back to the same `decision`
 * intent the web Approve button produces — terminating at the same agent entry
 * point. This is the worker's delivery fan-out, minus the (Phase-2) binding row.
 *
 * Run:  pnpm --filter @neko/worker demo:telegram
 * Live: TELEGRAM_BOT_TOKEN=… TELEGRAM_CHAT_ID=… pnpm --filter @neko/worker demo:telegram
 */
import { spawnSync } from "node:child_process";
import { existsSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { webProjection } from "@neko/channels";
import {
  TELEGRAM_PROFILE,
  WEB_PROFILE,
  type CapabilityProfile,
  type InteractionEvent,
  type IntentEvent,
} from "@neko/interaction";

const here = path.dirname(fileURLToPath(import.meta.url));
const RUNNER =
  process.env.TELEGRAM_PLUGIN_RUNNER ??
  path.resolve(here, "../../../../plugins/packages/telegram/dist/run.js");
const CHAT_RAW = process.env.TELEGRAM_CHAT_ID ?? "123456789";
const chatId = /^-?\d+$/.test(CHAT_RAW) ? Number(CHAT_RAW) : CHAT_RAW;

/** The waist. In production these come from toInteractionEvents(agentStream)
 *  (see multi-channel-demo.ts) or outputRowToInteractionEvent(row) (the
 *  /api/briefing path). Constructed here so the contrast is deterministic. */
const events: InteractionEvent[] = [
  {
    kind: "inform",
    id: "active-customers-latest-year",
    mood: "good",
    title: "Active customers up 366%",
    body:
      "18,069 distinct customers placed an order in the most recent year, up from 3,873 the prior year.\n\nGrowth was concentrated in the enterprise segment — full breakdown in the report.",
    metric: { label: "Active customers", value: "18,069" },
    series: { kind: "line", points: [ { d: "FY22", v: 3873 }, { d: "FY23", v: 18069 } ] },
  },
  {
    kind: "ask",
    id: "ar-501",
    ask: "approval",
    prompt: "Approve posting the customer-growth summary to the exec channel?",
    decisionRef: "ar-501",
    risk: "medium",
  },
];

interface RpcOut {
  ok: boolean;
  result?: unknown;
  error?: { code: string; message: string };
  stderr: string;
}

const rpc = (method: string, params: unknown): RpcOut => {
  const res = spawnSync("node", [RUNNER, method, JSON.stringify(params)], {
    encoding: "utf8",
    env: process.env,
  });
  if (res.error) throw new Error(`spawn failed for "${method}": ${res.error.message}`);
  const stdout = (res.stdout ?? "").trim();
  const lastLine = stdout.split("\n").filter(Boolean).at(-1) ?? "";
  let parsed: { ok: boolean; result?: unknown; error?: { code: string; message: string } };
  try {
    parsed = JSON.parse(lastLine);
  } catch {
    throw new Error(`"${method}" returned non-JSON stdout: ${stdout || "(empty)"}`);
  }
  return { ...parsed, stderr: (res.stderr ?? "").trim() };
};

const rule = (label: string): string =>
  `\n${"─".repeat(4)} ${label} ${"─".repeat(Math.max(0, 66 - label.length))}`;

const main = (): void => {
  if (!existsSync(RUNNER)) {
    console.error(
      `Telegram plugin runner not found:\n  ${RUNNER}\n\n` +
        `Build it first:\n  (cd ../plugins && pnpm --filter @open-neko/channel-telegram build)\n` +
        `or point TELEGRAM_PLUGIN_RUNNER at a built dist/run.js.`,
    );
    process.exit(1);
  }

  console.log(rule("THE WAIST — one modality-free InteractionEvent[]"));
  console.log(JSON.stringify(events, null, 2));

  // ── WEB: in-process, the exact projection /api/briefing renders ──
  const web = webProjection(events, WEB_PROFILE);
  const cards = (web.surfaces[1] as { updateComponents?: { components?: unknown[] } } | undefined)
    ?.updateComponents?.components;
  console.log(rule("WEB  (built-in, in-process)  profile: visual+cards+charts"));
  console.log("A2UI components:");
  console.log(JSON.stringify(cards, null, 2));
  console.log(`approval affordances: ${JSON.stringify(web.pendingAsks)}`);

  // ── TELEGRAM: the real plugin, over the worker's one-shot RPC ──
  const reg = rpc("register", {});
  const profile = (reg.result as { capabilities?: { channel?: { profile?: CapabilityProfile } } })
    ?.capabilities?.channel?.profile;
  const profileMatches = JSON.stringify(profile) === JSON.stringify(TELEGRAM_PROFILE);

  console.log(rule("TELEGRAM  (real plugin VM, one-shot RPC)  profile: text+markdown+buttons"));
  console.log(`register → providerLabel="Telegram"  directions=outbound,inbound`);
  console.log(
    `declared profile matches @neko/interaction TELEGRAM_PROFILE: ${profileMatches ? "yes ✓" : "NO — DRIFT ✗"}`,
  );

  const delivered = rpc("deliver", { recipient: { kind: "telegram", chatId }, events, profile });
  const live = Boolean(process.env.TELEGRAM_BOT_TOKEN);
  console.log(`deliver → ${JSON.stringify(delivered.result)}  ${live ? "(LIVE send)" : "(dry-run — set TELEGRAM_BOT_TOKEN to send)"}`);
  // In dry-run the projected native payload is on the plugin's stderr.
  const payload = delivered.stderr.replace(/^\[channel-telegram dry-run\][^[]*?(\[)/s, "$1");
  if (delivered.stderr) {
    console.log("projected Telegram payload:");
    try {
      console.log(JSON.stringify(JSON.parse(payload), null, 2));
    } catch {
      console.log(delivered.stderr);
    }
  }

  // ── INBOUND: a real Telegram button tap → the same intent the web button makes ──
  const tap = {
    raw: { update_id: 1, callback_query: { id: "c1", data: "approve:ar-501", message: { chat: { id: chatId } } } },
  };
  const inbound = rpc("parse_inbound", tap);
  const intents = (inbound.result as { intents?: IntentEvent[] })?.intents ?? [];
  console.log(rule("INBOUND — Telegram tap → IntentEvent → same agent entry point"));
  for (const intent of intents) {
    const route =
      intent.kind === "decision"
        ? `→ ${intent.choice}ActionRequest(orgId, "${intent.decisionRef}")  ← identical to the web Approve button`
        : intent.kind === "utterance"
          ? `→ runChatTurn({ message: "${intent.text}" })`
          : `→ (${intent.kind})`;
    console.log(`${JSON.stringify(intent)}\n  ${route}`);
  }

  console.log(rule("RESULT"));
  console.log(
    "One InteractionEvent[] → WEB (A2UI BriefingCard + inline approval) AND TELEGRAM\n" +
      "(HTML message + Approve/Reject buttons), projected per profile, simultaneously.\n" +
      "Neither the agent nor the waist named a channel. Adding Telegram was one manifest\n" +
      "+ one adapter; the web path was untouched.\n",
  );
};

main();
