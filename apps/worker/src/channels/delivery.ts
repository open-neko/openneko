// Channel delivery + inbound dispatch — the worker glue that turns the V2
// channel contract into a live capability. Outbound: a newly-emitted workflow
// output fans out to every enabled delivery_binding via the plugin's deliver
// RPC. Inbound: an IntentEvent (parsed in-VM) routes to the SAME agent entry
// points the web uses — approve/reject an action_request, or start a chat run.
import { and, db, delivery_binding, eq, processing_job } from "@neko/db";
import { enqueue, QUEUE } from "@neko/db/jobs";
import { resolveAgentBackend } from "@neko/llm";
import { outputRowToInteractionEvent, type OutputRow } from "@neko/llm/interaction";
import type { InteractionEvent } from "@neko/interaction";
import {
  approveActionRequest,
  getActionRequest,
  rejectActionRequest,
  setWorkflowOutputDeliveryHook,
  type WorkflowOutputDeliveryHook,
} from "@neko/llm/workflows";
import {
  createWorkRun,
  createWorkThread,
  type RunChannel,
} from "@neko/llm/work";
import { getPluginRegistryInstance } from "../plugins/registry-instance.js";

/** "@open-neko/channel-telegram" → "telegram". Channel-inbound runs are never
 *  "web", so they get no a2ui rendering. See docs/PER_CHANNEL_RENDERING.md. */
function channelFromPlugin(pluginName: string): RunChannel {
  const m = pluginName.match(/channel-([a-z0-9-]+)$/i);
  return (m ? m[1].toLowerCase() : pluginName) as RunChannel;
}

type IntentEvent =
  | { kind: "utterance"; threadRef?: string; text: string }
  | { kind: "decision"; decisionRef: string; choice: "approve" | "reject"; reason?: string }
  | { kind: "select"; ref: string; optionId: string }
  | { kind: "invoke"; command: string; args?: Record<string, unknown> };

const MOODS = ["good", "watch", "act"] as const;

const onOutput: WorkflowOutputDeliveryHook = async (orgId, output) => {
  const reg = getPluginRegistryInstance();
  if (!reg) return;
  const bindings = await db()
    .select()
    .from(delivery_binding)
    .where(
      and(eq(delivery_binding.org_id, orgId), eq(delivery_binding.enabled, true)),
    );
  if (bindings.length === 0) return;
  const mood = MOODS.find((m) => m === output.mood);
  const row: OutputRow = {
    id: output.id,
    title: output.title,
    body: output.body,
    ...(mood ? { mood } : {}),
  };
  const event = outputRowToInteractionEvent(row);
  for (const b of bindings) {
    try {
      const res = await reg.deliverOnChannel(
        b.channel_plugin,
        (b.recipient ?? {}) as Record<string, unknown>,
        [event],
      );
      console.log(
        `[channel-delivery] ${b.channel_plugin} output=${output.id} delivered=${res.delivered}${res.ref ? ` ref=${res.ref}` : ""}`,
      );
    } catch (err) {
      console.warn(
        `[channel-delivery] ${b.channel_plugin} output=${output.id} failed: ${err instanceof Error ? err.message : err}`,
      );
    }
  }
};

/** Register the output→channel fan-out hook. Called once at worker startup. */
export function registerChannelOutputDelivery(): void {
  setWorkflowOutputDeliveryHook(onOutput);
}

/**
 * Send a chat run's reply back to the channel that asked. Channel-initiated
 * runs (Telegram, …) have no other return path — the web UI streams its runs
 * over SSE, but a Telegram sender only hears back if we deliver here.
 */
export async function deliverChatReply(
  channelPlugin: string,
  recipient: Record<string, unknown>,
  runId: string,
  body: string,
): Promise<void> {
  const reg = getPluginRegistryInstance();
  if (!reg || !body.trim()) return;
  // A chat reply is the assistant's message, not a workflow-output card — use
  // `converse` so channels render the full text, not a summarized headline.
  const event: InteractionEvent = {
    kind: "converse",
    id: runId,
    role: "assistant",
    text: body,
  };
  try {
    const res = await reg.deliverOnChannel(channelPlugin, recipient, [event]);
    console.log(
      `[channel-delivery] ${channelPlugin} chat-reply run=${runId} delivered=${res.delivered}${res.ref ? ` ref=${res.ref}` : ""}`,
    );
  } catch (err) {
    console.warn(
      `[channel-delivery] ${channelPlugin} chat-reply run=${runId} failed: ${err instanceof Error ? err.message : err}`,
    );
  }
}

/**
 * Auto-bind delivery to a sender on first inbound contact: the operator DMs the
 * bot once and starts receiving outputs there, never hand-writing a binding.
 * Idempotent — only writes when no binding for (org, channel) exists yet.
 */
export async function ensureInboundBinding(
  orgId: string,
  channelPlugin: string,
  recipient: Record<string, unknown>,
): Promise<void> {
  const existing = await db()
    .select({ id: delivery_binding.id })
    .from(delivery_binding)
    .where(
      and(
        eq(delivery_binding.org_id, orgId),
        eq(delivery_binding.channel_plugin, channelPlugin),
      ),
    )
    .limit(1);
  if (existing.length > 0) return;
  await db()
    .insert(delivery_binding)
    .values({ org_id: orgId, channel_plugin: channelPlugin, recipient, enabled: true });
  console.log(
    `[channel-inbound] auto-bound ${channelPlugin} → ${JSON.stringify(recipient)} (first inbound from this sender)`,
  );
}

/** Route one inbound IntentEvent to the same agent entry points the web uses. */
export async function dispatchInboundIntent(
  orgId: string,
  intent: IntentEvent,
  channelPlugin: string,
  recipient?: Record<string, unknown>,
): Promise<void> {
  if (intent.kind === "decision") {
    const req = await getActionRequest(orgId, intent.decisionRef);
    if (!req) {
      console.warn(
        `[channel-inbound] decision for unknown action_request ${intent.decisionRef}`,
      );
      return;
    }
    const pending = req.status === "pending_approval" || req.status === "draft";
    // Idempotent: only the call that finds the request still pending acts on it,
    // so a duplicate tap / webhook retry / poller overlap can't double-execute.
    if (!pending) {
      console.log(
        `[channel-inbound] action_request ${req.id} already ${req.status}; ignoring duplicate ${intent.choice}`,
      );
      return;
    }
    if (intent.choice === "approve") {
      await approveActionRequest({ id: req.id, orgId, approverUserId: null });
      await enqueue(QUEUE.ACTION_EXECUTE, { orgId, actionRequestId: req.id });
      console.log(`[channel-inbound] approved + queued action_request ${req.id}`);
    } else {
      await rejectActionRequest({
        id: req.id,
        orgId,
        approverUserId: null,
        reason: intent.reason,
      });
      console.log(`[channel-inbound] rejected action_request ${req.id}`);
    }
    return;
  }
  if (intent.kind === "utterance" || intent.kind === "invoke") {
    const message =
      intent.kind === "utterance"
        ? intent.text
        : `/${intent.command}${intent.args?.text ? ` ${String(intent.args.text)}` : ""}`;
    await startChatRun(
      orgId,
      message,
      channelFromPlugin(channelPlugin),
      channelPlugin,
      recipient,
      intent.kind === "utterance" ? intent.threadRef : undefined,
    );
    return;
  }
  console.log(
    `[channel-inbound] select ${intent.ref}=${intent.optionId} (no handler wired)`,
  );
}

async function startChatRun(
  orgId: string,
  message: string,
  channel: RunChannel,
  channelPlugin: string,
  recipient: Record<string, unknown> | undefined,
  threadRef?: string,
): Promise<void> {
  const thread = await createWorkThread(
    orgId,
    threadRef ? `Telegram ${threadRef}` : "Telegram",
    channel,
  );
  const backend = await resolveAgentBackend(orgId);
  const run = await createWorkRun(orgId, thread.id, backend.id);
  const inserted = await db()
    .insert(processing_job)
    .values({
      org_id: orgId,
      kind: "work_run",
      status: "queued",
      trigger: "channel_inbound",
      trigger_payload: { message },
    })
    .returning({ id: processing_job.id });
  const processingJobId = inserted[0]?.id;
  if (!processingJobId) return;
  await enqueue(QUEUE.WORK_RUN, {
    processingJobId,
    orgId,
    runId: run.id,
    threadId: thread.id,
    message,
    channel,
    channelPlugin,
    recipient,
  });
  console.log(
    `[channel-inbound] started chat run ${run.id} for "${message.slice(0, 40)}"`,
  );
}

/** Webhook ingest: verify (in-VM) → parse (in-VM) → dispatch. */
export async function ingestInboundWebhook(
  orgId: string,
  pluginName: string,
  headers: Record<string, string>,
  rawBody: string,
): Promise<{ ok: boolean; dispatched: number }> {
  const reg = getPluginRegistryInstance();
  if (!reg) return { ok: false, dispatched: 0 };
  const verified = await reg.verifyInbound(pluginName, headers, rawBody);
  if (!verified) return { ok: false, dispatched: 0 };
  let raw: unknown;
  try {
    raw = JSON.parse(rawBody);
  } catch {
    return { ok: false, dispatched: 0 };
  }
  const { intents, recipient } = await reg.parseInbound(pluginName, raw);
  if (recipient) await ensureInboundBinding(orgId, pluginName, recipient);
  for (const intent of intents) {
    try {
      await dispatchInboundIntent(
        orgId,
        intent as IntentEvent,
        pluginName,
        recipient as Record<string, unknown> | undefined,
      );
    } catch (err) {
      console.warn(
        `[channel-inbound] dispatch error: ${err instanceof Error ? err.message : err}`,
      );
    }
  }
  return { ok: true, dispatched: intents.length };
}
