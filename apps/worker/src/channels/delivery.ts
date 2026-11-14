// Inbound channel dispatch: an IntentEvent routes to the same agent entry
// points the web uses: approve or reject an action_request, or start a chat
// run. openneko web sends every outbound message.
import { and, db, delivery_binding, eq, processing_job } from "@neko/db";
import { enqueueActionExecute, enqueueChannelDeliver, enqueueWorkRun } from "@neko/db/jobs";
import { resolveAgentBackend } from "@neko/llm";
import { SpendBudgetExceeded } from "@neko/llm/spend";
import type { InteractionEvent } from "@neko/interaction";
import {
  approveActionRequest,
  getActionRequest,
  rejectActionRequest,
} from "@neko/llm/workflows";
import {
  channelThreadId,
  createWorkMessage,
  createWorkRun,
  createWorkThread,
  getOrCreateChannelThread,
  getWorkThread,
  type RunChannel,
} from "@neko/llm/work";
import { builtInChannel } from "@neko/channels";
import { parseChannelInbound, verifyChannelInbound } from "./runtime.js";
import {
  beginInboundUpdate,
  inboundUpdateKey,
  markInboundDone,
  recordInboundFailure,
} from "./inbound-store.js";

/** Channel ids are run channels ("telegram", "slack", …). Channel-inbound runs
 *  are never "web", so they get no a2ui rendering. See docs/PER_CHANNEL_RENDERING.md. */
function channelFromPlugin(channelId: string): RunChannel {
  return channelId as RunChannel;
}

// Stable per-conversation key from the channel-native recipient (+ thread),
// so the same DM / channel thread maps to one work_thread across turns.
export function conversationKeyFor(
  recipient: Record<string, unknown>,
  threadRef?: string,
): string {
  const base =
    (typeof recipient.channel === "string" && recipient.channel) ||
    (recipient.chatId != null && String(recipient.chatId)) ||
    (typeof recipient.to === "string" && recipient.to) ||
    "";
  if (!base) return "";
  return threadRef ? `${base}:${threadRef}` : base;
}

type IntentEvent =
  | { kind: "utterance"; threadRef?: string; text: string }
  | { kind: "decision"; decisionRef: string; choice: "approve" | "reject"; reason?: string }
  | { kind: "select"; ref: string; optionId: string }
  | { kind: "invoke"; command: string; args?: Record<string, unknown> };

async function enqueueChannelDelivery(
  orgId: string,
  channelPlugin: string,
  recipient: Record<string, unknown>,
  events: unknown[],
  idempotencyKey: string,
): Promise<void> {
  await enqueueChannelDeliver({ orgId, channelPlugin, recipient, events }, idempotencyKey);
}

/**
 * Send a plain text reply to the channel that asked, such as the spend
 * limit notice. `openneko web` sends the answer of a channel run itself.
 */
export async function deliverChatReply(
  orgId: string,
  channelPlugin: string,
  recipient: Record<string, unknown>,
  replyId: string,
  body: string,
): Promise<void> {
  if (!body.trim()) return;
  const event: InteractionEvent = { kind: "converse", id: replyId, role: "assistant", text: body };
  await enqueueChannelDelivery(orgId, channelPlugin, recipient, [event], `reply-${replyId}`);
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

/**
 * CH2: resolve the org an inbound update belongs to from the sender's
 * workspace scope. Unknown workspace = first contact: auto-bind to the
 * default org (single-tenant behavior preserved); a multi-tenant install
 * remaps channel_workspace rows explicitly.
 */
export async function resolveInboundOrg(
  channelPlugin: string,
  workspaceId: string | undefined,
  defaultOrgId: string,
): Promise<string> {
  if (!workspaceId) return defaultOrgId;
  const { channel_workspace } = await import("@neko/db");
  const [row] = await db()
    .select({ orgId: channel_workspace.org_id })
    .from(channel_workspace)
    .where(
      and(
        eq(channel_workspace.channel_plugin, channelPlugin),
        eq(channel_workspace.workspace_id, workspaceId),
      ),
    )
    .limit(1);
  if (row) return row.orgId;
  await db()
    .insert(channel_workspace)
    .values({
      org_id: defaultOrgId,
      channel_plugin: channelPlugin,
      workspace_id: workspaceId,
    })
    .onConflictDoNothing();
  console.log(
    `[channel-inbound] auto-bound workspace ${channelPlugin}/${workspaceId} → org ${defaultOrgId}`,
  );
  return defaultOrgId;
}

/** Route one inbound IntentEvent to the same agent entry points the web uses. */
export async function dispatchInboundIntent(
  orgId: string,
  intent: IntentEvent,
  channelPlugin: string,
  recipient?: Record<string, unknown>,
  sender?: { id: string; displayName?: string; workspaceId?: string; email?: string },
): Promise<void> {
  // CH3: a linked sender acts as their app_user; unlinked stays
  // anonymous member-grade; blocked identities are dropped outright.
  const { resolveChannelActor } = await import("./identity.js");
  const actor = await resolveChannelActor(orgId, channelPlugin, sender);
  if (actor.blocked) {
    console.warn(
      `[channel-inbound] dropped inbound from blocked identity ${channelPlugin}/${sender?.id}`,
    );
    return;
  }
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
      await approveActionRequest({
        id: req.id,
        orgId,
        approverUserId: actor.userId,
        // K2: the resolved CH3 actor decides the approval grade — an
        // unlinked sender stays member-grade, so admin-gated policies
        // still block approval from an anonymous chat surface.
        approver: { userId: actor.userId, role: actor.role },
      });
      await enqueueActionExecute({ orgId, actionRequestId: req.id });
      console.log(`[channel-inbound] approved + queued action_request ${req.id}`);
    } else {
      await rejectActionRequest({
        id: req.id,
        orgId,
        approverUserId: actor.userId,
        reason: intent.reason,
        approver: { userId: actor.userId, role: actor.role },
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
      sender,
      actor,
    );
    return;
  }
  // Other channels may still emit a raw select; continue the conversation with
  // the chosen option instead of dropping it (Slack sends the label as text).
  await startChatRun(
    orgId,
    intent.optionId,
    channelFromPlugin(channelPlugin),
    channelPlugin,
    recipient,
    undefined,
    sender,
    actor,
  );
}

async function startChatRun(
  orgId: string,
  message: string,
  channel: RunChannel,
  channelPlugin: string,
  recipient: Record<string, unknown> | undefined,
  threadRef?: string,
  sender?: { id: string; displayName?: string; workspaceId?: string; email?: string },
  actor: { userId: string | null; role: "admin" | "member" } = {
    userId: null,
    role: "member",
  },
): Promise<void> {
  const channelLabel = channel.charAt(0).toUpperCase() + channel.slice(1);
  const title = threadRef ? `${channelLabel} ${threadRef}` : channelLabel;
  const conversationKey = recipient ? conversationKeyFor(recipient, threadRef) : "";
  // W3.2: a bare in-thread channel reply only continues a thread the bot already
  // owns — drop it when no matching work_thread exists (DMs and @-mentions still
  // create one).
  if (recipient?.requireThread) {
    const existing = conversationKey
      ? await getWorkThread(orgId, channelThreadId(orgId, channel, conversationKey))
      : null;
    if (!existing) return;
  }
  const thread = conversationKey
    ? await getOrCreateChannelThread({
        orgId,
        channel,
        conversationKey,
        title,
        createdByUserId: actor.userId ?? undefined,
      })
    : await createWorkThread(orgId, title, channel, actor.userId ?? undefined);
  const backend = await resolveAgentBackend(orgId);
  // K1: the CH3-resolved actor is the run's acting principal — a linked
  // sender gets their personal layer and role, unlinked stays anonymous.
  let run: Awaited<ReturnType<typeof createWorkRun>>;
  try {
    run = await createWorkRun(orgId, thread.id, backend.id, actor, { source: "channel" });
  } catch (error) {
    if (!(error instanceof SpendBudgetExceeded) || !recipient) throw error;
    await deliverChatReply(orgId, channelPlugin, recipient, `spend-${thread.id}-${Date.now()}`, error.message);
    return;
  }
  // Record the user turn so reused threads build real conversation history.
  await createWorkMessage({
    orgId,
    threadId: thread.id,
    runId: run.id,
    role: "user",
    content: message,
  });
  const inserted = await db()
    .insert(processing_job)
    .values({
      org_id: orgId,
      kind: "work_run",
      status: "queued",
      trigger: "channel_inbound",
      // CH1: persist who sent it; K1 resolves to an actor, CH3 links it.
      trigger_payload: sender ? { message, sender } : { message },
    })
    .returning({ id: processing_job.id });
  const processingJobId = inserted[0]?.id;
  if (!processingJobId) return;
  await enqueueWorkRun({
    processingJobId,
    orgId,
    runId: run.id,
    threadId: thread.id,
    message,
    channel,
    channelPlugin,
    recipient,
    ...(sender ? { sender } : {}),
  });
  console.log(
    `[channel-inbound] started chat run ${run.id} for "${message.slice(0, 40)}"`,
  );
}

// A persistently-failing update is retried up to this many times (with poll
// backoff between attempts) before it's dead-lettered and the cursor advances
// past it. The count is persisted, so the budget survives restarts.
const MAX_INBOUND_ATTEMPTS = 30;

/**
 * Parse + dispatch one raw inbound update, exactly once. The update is claimed
 * in the ledger first: a duplicate (restart re-poll, webhook retry) or a
 * dead-lettered update is skipped. A transient failure is recorded and retried;
 * after MAX_INBOUND_ATTEMPTS it's dead-lettered and consumed. Returns false
 * ONLY while a failure is still retrying (caller holds the cursor); true once
 * the update is done, duplicate, or dead-lettered. Shared by poll + webhook.
 */
export async function processInboundUpdate(
  orgId: string,
  pluginName: string,
  raw: unknown,
): Promise<boolean> {
  const key = inboundUpdateKey(raw);
  const begin = await beginInboundUpdate(orgId, pluginName, key);
  if (!begin.proceed) {
    console.log(
      `[channel-inbound] ${pluginName}: skipping ${begin.dead ? "dead-lettered" : "duplicate"} inbound update`,
    );
    return true;
  }
  try {
    const { intents, recipient, sender } = await parseChannelInbound(pluginName, raw);
    // CH2: the sender's workspace decides the org; first contact binds
    // to the caller's default org.
    const resolvedOrgId = await resolveInboundOrg(
      pluginName,
      sender?.workspaceId,
      orgId,
    );
    if (recipient) {
      await ensureInboundBinding(resolvedOrgId, pluginName, recipient);
    }
    for (const intent of intents) {
      await dispatchInboundIntent(
        resolvedOrgId,
        intent as IntentEvent,
        pluginName,
        recipient as Record<string, unknown> | undefined,
        sender,
      );
    }
    await markInboundDone(orgId, pluginName, key);
    return true;
  } catch (err) {
    const errMsg = err instanceof Error ? err.message : String(err);
    const { dead, attempts } = await recordInboundFailure(
      orgId,
      pluginName,
      key,
      MAX_INBOUND_ATTEMPTS,
      raw,
      errMsg,
    );
    if (dead) {
      console.error(
        `[channel-inbound] ${pluginName}: dead-lettered update after ${attempts} attempt(s): ${errMsg}`,
      );
      return true; // give up — advance past the poison update
    }
    console.warn(
      `[channel-inbound] ${pluginName} dispatch error (attempt ${attempts}/${MAX_INBOUND_ATTEMPTS}): ${errMsg}`,
    );
    return false; // hold the cursor; retry next poll with backoff
  }
}

/** Webhook ingest: verify → parse → dispatch, deduped. */
export async function ingestInboundWebhook(
  orgId: string,
  pluginName: string,
  headers: Record<string, string>,
  rawBody: string,
): Promise<{ ok: boolean; dispatched: number }> {
  // Webhooks registered while the channel was a plugin still post to its package name.
  const channelId = builtInChannel(pluginName)?.id;
  if (!channelId) return { ok: false, dispatched: 0 };
  const verified = await verifyChannelInbound(channelId, headers, rawBody);
  if (!verified) return { ok: false, dispatched: 0 };
  let raw: unknown;
  try {
    raw = JSON.parse(rawBody);
  } catch {
    return { ok: false, dispatched: 0 };
  }
  const ok = await processInboundUpdate(orgId, channelId, raw);
  return { ok, dispatched: ok ? 1 : 0 };
}
