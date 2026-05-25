// Local inbound for Telegram without a public URL. Telegram webhooks need a
// public HTTPS endpoint; for `pnpm dev` we instead long-poll getUpdates. This
// is the doc's sanctioned "thin worker-side ingress owns the socket/poll while
// projection + parsing stay in the VM" path: the worker does the GET, but each
// raw Update is normalized to IntentEvents by the plugin's parse_inbound RPC.
// Opt-in via TELEGRAM_INBOUND_POLL=1.
import { getPluginRegistryInstance } from "../plugins/registry-instance.js";
import { dispatchInboundIntent } from "./delivery.js";

const TELEGRAM_PLUGIN = "@open-neko/channel-telegram";

type IntentEvent = Parameters<typeof dispatchInboundIntent>[1];

export function startTelegramInboundPoll(orgId: string): { stop: () => void } | null {
  if (process.env.TELEGRAM_INBOUND_POLL !== "1") return null;
  const reg = getPluginRegistryInstance();
  const token = reg?.getPluginEnv(TELEGRAM_PLUGIN)?.TELEGRAM_BOT_TOKEN;
  if (!reg || !token) {
    console.warn(
      "[telegram-poll] TELEGRAM_INBOUND_POLL=1 but no bot token / registry; poller disabled",
    );
    return null;
  }

  let offset = 0;
  let stopped = false;

  const loop = async (): Promise<void> => {
    console.log("[telegram-poll] inbound poller started (getUpdates long-poll)");
    while (!stopped) {
      try {
        const url = `https://api.telegram.org/bot${token}/getUpdates?timeout=25&offset=${offset}`;
        const res = await fetch(url, { signal: AbortSignal.timeout(30_000) });
        const data = (await res.json()) as {
          ok: boolean;
          result?: Array<{ update_id: number }>;
        };
        if (data.ok && data.result) {
          for (const update of data.result) {
            offset = update.update_id + 1;
            const u = update as {
              message?: { chat?: { id?: number | string } };
              callback_query?: { message?: { chat?: { id?: number | string } } };
            };
            const chatId = u.message?.chat?.id ?? u.callback_query?.message?.chat?.id;
            console.log(
              `[telegram-poll] update ${update.update_id} from chat ${chatId ?? "?"}`,
            );
            try {
              const intents = (await reg.parseInbound(
                TELEGRAM_PLUGIN,
                update,
              )) as IntentEvent[];
              for (const intent of intents) {
                await dispatchInboundIntent(orgId, intent);
              }
            } catch (err) {
              console.warn(
                `[telegram-poll] dispatch error: ${err instanceof Error ? err.message : err}`,
              );
            }
          }
        }
      } catch (err) {
        if (!stopped) {
          console.warn(
            `[telegram-poll] getUpdates error: ${err instanceof Error ? err.message : err}`,
          );
          await new Promise((r) => setTimeout(r, 3_000));
        }
      }
    }
  };

  void loop();
  return {
    stop: () => {
      stopped = true;
    },
  };
}
