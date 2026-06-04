// Inbound transport for channels on hosts without a public webhook URL. For every
// installed inbound-capable channel, the worker loops the plugin's poll_inbound
// RPC (provider-agnostic — no Telegram-specific code here), normalizes each raw
// update via parse_inbound, auto-binds delivery to the sender on first contact,
// and dispatches the intents to the same agent entry points the web uses.
//
// No env flag: a channel that declares an inbound direction is polled
// automatically. Channels reachable by a public webhook (OPENNEKO_PUBLIC_URL set
// + ingress="webhook") receive inbound via POST /channels/:plugin/inbound instead
// and are skipped here.
import { getPluginRegistryInstance } from "../plugins/registry-instance.js";
import { dispatchInboundIntent, ensureInboundBinding } from "./delivery.js";
import { pollBackoffMs, shouldLogPollFailure } from "./poll-backoff.js";

type IntentEvent = Parameters<typeof dispatchInboundIntent>[1];

const msg = (e: unknown): string => (e instanceof Error ? e.message : String(e));
const sleep = (ms: number): Promise<void> => new Promise((r) => setTimeout(r, ms));

/** Start inbound transports for all installed inbound channels. Returns a stop handle. */
export function startChannelInbound(orgId: string): { stop: () => void } {
  let stopped = false;
  const reg = getPluginRegistryInstance();
  if (!reg) return { stop: () => { stopped = true; } };

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
    let cursor: string | undefined;
    let warnedUnsupported = false;
    let failureStreak = 0;
    let lastError: string | undefined;
    console.log(`[channel-inbound] ${pluginName}: polling for inbound (no public webhook URL)`);
    while (!stopped) {
      try {
        const r = getPluginRegistryInstance();
        if (!r) return;
        const { updates, cursor: next } = await r.pollInbound(pluginName, cursor);
        if (next) cursor = next;
        for (const raw of updates) {
          try {
            const { intents, recipient } = await r.parseInbound(pluginName, raw);
            if (recipient) await ensureInboundBinding(orgId, pluginName, recipient);
            for (const intent of intents) {
              await dispatchInboundIntent(
                orgId,
                intent as IntentEvent,
                pluginName,
                recipient as Record<string, unknown> | undefined,
              );
            }
          } catch (err) {
            console.warn(`[channel-inbound] ${pluginName} dispatch error: ${msg(err)}`);
          }
        }
        if (failureStreak > 0) {
          console.log(
            `[channel-inbound] ${pluginName}: poll recovered after ${failureStreak} failed attempt(s)`,
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

  return { stop: () => { stopped = true; } };
}
