// Inbound transport for channels on hosts without a public webhook URL. For every
// installed inbound-capable channel, the worker loops the plugin's poll_inbound
// RPC (provider-agnostic — no Telegram-specific code here), then hands each raw
// update to processInboundUpdate, which dedupes and dispatches it to the same
// agent entry points the web uses.
//
// Reliability across restarts: the poll cursor is persisted per (org, channel),
// so a restart resumes from the last acknowledged offset; the cursor only
// advances when the whole batch dispatched cleanly, and the dedup ledger makes
// re-polling a partially-processed batch safe (exactly-once dispatch).
//
// No env flag: a channel that declares an inbound direction is polled
// automatically. Channels reachable by a public webhook (OPENNEKO_PUBLIC_URL set
// + ingress="webhook") receive inbound via POST /channels/:plugin/inbound instead
// and are skipped here.
import { getPluginRegistryInstance } from "../plugins/registry-instance.js";
import { processInboundUpdate } from "./delivery.js";
import {
  loadPollCursor,
  pruneInboundDedup,
  savePollCursor,
} from "./inbound-store.js";
import { pollBackoffMs, shouldLogPollFailure } from "./poll-backoff.js";

const DEDUP_PRUNE_INTERVAL_MS = 60 * 60 * 1000;

const msg = (e: unknown): string => (e instanceof Error ? e.message : String(e));
const sleep = (ms: number): Promise<void> => new Promise((r) => setTimeout(r, ms));

type Poller = {
  pollInbound: (
    pluginName: string,
    cursor?: string,
  ) => Promise<{ updates: unknown[]; cursor?: string }>;
};

/**
 * One poll step: fetch a batch, dispatch each update exactly-once, and advance
 * the cursor only when the whole batch settled (each update done, duplicate, or
 * dead-lettered). A still-retrying update returns `held: true` with the cursor
 * unchanged, so the batch is re-polled — the ledger skips updates that already
 * settled, and the caller backs off before the next poll.
 */
export async function runPollIteration(
  orgId: string,
  pluginName: string,
  cursor: string | undefined,
  poller: Poller,
): Promise<{ cursor: string | undefined; held: boolean }> {
  const { updates, cursor: next } = await poller.pollInbound(pluginName, cursor);
  let held = false;
  for (const raw of updates) {
    if (!(await processInboundUpdate(orgId, pluginName, raw))) held = true;
  }
  if (!held && next && next !== cursor) {
    await savePollCursor(orgId, pluginName, next);
    return { cursor: next, held: false };
  }
  return { cursor, held };
}

/** Start inbound transports for all installed inbound channels. Returns a stop handle. */
export function startChannelInbound(orgId: string): { stop: () => void } {
  let stopped = false;
  const reg = getPluginRegistryInstance();
  if (!reg) return { stop: () => { stopped = true; } };

  const pruneTimer = setInterval(() => {
    void pruneInboundDedup(Date.now())
      .then((n) => {
        if (n > 0) console.log(`[channel-inbound] pruned ${n} expired dedup row(s)`);
      })
      .catch((e) => console.warn(`[channel-inbound] dedup prune error: ${msg(e)}`));
  }, DEDUP_PRUNE_INTERVAL_MS);
  pruneTimer.unref?.();

  const hasPublicWebhook = Boolean(process.env.OPENNEKO_PUBLIC_URL);
  const inboundProviders = reg
    .getChannelProviders()
    .filter((p) => p.directions.includes("inbound"));

  for (const provider of inboundProviders) {
    if (hasPublicWebhook && provider.ingress === "webhook") {
      console.log(
        `[channel-inbound] ${provider.pluginName}: webhook ingress at ${process.env.OPENNEKO_PUBLIC_URL}/channels/${encodeURIComponent(provider.pluginName)}/inbound`,
      );
      continue;
    }
    void pollLoop(provider.pluginName);
  }

  async function pollLoop(pluginName: string): Promise<void> {
    let cursor = await loadPollCursor(orgId, pluginName);
    let warnedUnsupported = false;
    let failureStreak = 0;
    let lastError: string | undefined;
    console.log(
      `[channel-inbound] ${pluginName}: polling for inbound (no public webhook URL)${cursor ? " — resumed from saved cursor" : ""}`,
    );
    while (!stopped) {
      try {
        const r = getPluginRegistryInstance();
        if (!r) return;
        const res = await runPollIteration(orgId, pluginName, cursor, r);
        cursor = res.cursor;
        if (res.held) {
          // A still-retrying update holds the cursor; back off (shared streak)
          // so we don't re-poll the failing batch every base interval.
          failureStreak++;
          if (shouldLogPollFailure(failureStreak, failureStreak === 1)) {
            console.warn(
              `[channel-inbound] ${pluginName}: holding cursor, retrying failed update(s) (attempt ${failureStreak})`,
            );
          }
        } else if (failureStreak > 0) {
          console.log(
            `[channel-inbound] ${pluginName}: recovered after ${failureStreak} held/failed attempt(s)`,
          );
          failureStreak = 0;
          lastError = undefined;
        }
      } catch (err) {
        const m = msg(err);
        if (m.includes("does not implement poll_inbound")) {
          if (!warnedUnsupported) {
            console.warn(
              `[channel-inbound] ${pluginName}: no poll transport and no public webhook URL — inbound disabled. Set OPENNEKO_PUBLIC_URL to use webhook ingress.`,
            );
            warnedUnsupported = true;
          }
          return; // can't be polled; retrying won't help
        }
        if (!stopped) {
          failureStreak++;
          if (shouldLogPollFailure(failureStreak, m !== lastError)) {
            const attempt = failureStreak > 1 ? ` (attempt ${failureStreak})` : "";
            console.warn(`[channel-inbound] ${pluginName} poll error${attempt}: ${m}`);
          }
          lastError = m;
        }
      }
      if (!stopped) await sleep(pollBackoffMs(failureStreak));
    }
  }

  return {
    stop: () => {
      stopped = true;
      clearInterval(pruneTimer);
    },
  };
}
