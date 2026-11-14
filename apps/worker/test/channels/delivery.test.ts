import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

// delivery.ts wires several cross-package seams; mock them so the dispatch
// logic (idempotency + routing), durable delivery, and inbound dedup are
// tested in isolation.
vi.mock("@neko/llm/workflows", () => ({
  getActionRequest: vi.fn(),
  approveActionRequest: vi.fn(async () => ({})),
  rejectActionRequest: vi.fn(async () => ({})),
}));
vi.mock("@neko/db/jobs", () => ({
  enqueueActionExecute: vi.fn(async () => "1"),
  enqueueChannelDeliver: vi.fn(async () => "2"),
  enqueueWorkRun: vi.fn(async () => "3"),
}));
const h = vi.hoisted(() => ({
  binds: [] as Array<Record<string, unknown>>,
  selectRows: [] as unknown[],
}));
vi.mock("@neko/db", () => ({
  db: vi.fn(() => ({
    insert: () => ({
      values: (v: Record<string, unknown>) => {
        h.binds.push(v);
        return { returning: async () => [{ id: "pj-1" }] };
      },
    }),
    select: () => ({
      from: () => ({
        where: () =>
          Object.assign(Promise.resolve(h.selectRows), {
            limit: async () => h.selectRows,
          }),
      }),
    }),
  })),
  and: (...a: unknown[]) => a,
  eq: (...a: unknown[]) => a,
  delivery_binding: { org_id: {}, enabled: {}, channel_plugin: {}, id: {} },
  processing_job: { id: {} },
}));
vi.mock("@neko/llm", () => ({ resolveAgentBackend: vi.fn(async () => ({ id: "ax" })) }));
vi.mock("@neko/llm/work", () => ({
  createWorkThread: vi.fn(async () => ({ id: "thread-1" })),
  getOrCreateChannelThread: vi.fn(async () => ({ id: "thread-1" })),
  createWorkRun: vi.fn(async () => ({ id: "run-1" })),
  createWorkMessage: vi.fn(async () => ({ id: "msg-1" })),
  channelThreadId: vi.fn(() => "tid-1"),
  getWorkThread: vi.fn(async () => null),
}));
vi.mock("../../src/channels/inbound-store.js", () => ({
  inboundUpdateKey: vi.fn(() => "update-key"),
  beginInboundUpdate: vi.fn(async () => ({ proceed: true, attempts: 0, dead: false })),
  markInboundDone: vi.fn(async () => {}),
  recordInboundFailure: vi.fn(async () => ({ dead: false, attempts: 1 })),
}));
vi.mock("../../src/channels/runtime.js", () => ({
  parseChannelInbound: vi.fn(),
  verifyChannelInbound: vi.fn(),
}));

import { enqueueActionExecute, enqueueChannelDeliver, enqueueWorkRun } from "@neko/db/jobs";
import {
  approveActionRequest,
  getActionRequest,
  rejectActionRequest,
} from "@neko/llm/workflows";
import { createWorkRun, getOrCreateChannelThread, getWorkThread } from "@neko/llm/work";
import {
  conversationKeyFor,
  deliverChatReply,
  dispatchInboundIntent,
  ensureInboundBinding,
  ingestInboundWebhook,
} from "../../src/channels/delivery";
import {
  beginInboundUpdate,
  markInboundDone,
  recordInboundFailure,
} from "../../src/channels/inbound-store.js";
import {
  parseChannelInbound,
  verifyChannelInbound,
} from "../../src/channels/runtime.js";

beforeEach(() => {
  vi.clearAllMocks();
  vi.mocked(beginInboundUpdate).mockResolvedValue({ proceed: true, attempts: 0, dead: false });
  vi.mocked(recordInboundFailure).mockResolvedValue({ dead: false, attempts: 1 });
});
afterEach(() => vi.clearAllMocks());

describe("dispatchInboundIntent — decision", () => {
  it("approve on a pending request approves + enqueues the action_execute job", async () => {
    vi.mocked(getActionRequest).mockResolvedValue({ id: "ar-1", status: "pending_approval", kind: "k" } as never);
    await dispatchInboundIntent("org-1", { kind: "decision", decisionRef: "ar-1", choice: "approve" });
    expect(approveActionRequest).toHaveBeenCalledWith({
      id: "ar-1",
      orgId: "org-1",
      approverUserId: null,
      approver: { userId: null, role: "member" },
    });
    expect(enqueueActionExecute).toHaveBeenCalledWith({ orgId: "org-1", actionRequestId: "ar-1" });
  });

  it("is idempotent — an already-executed request is NOT re-approved or re-enqueued", async () => {
    vi.mocked(getActionRequest).mockResolvedValue({ id: "ar-1", status: "executed", kind: "k" } as never);
    await dispatchInboundIntent("org-1", { kind: "decision", decisionRef: "ar-1", choice: "approve" });
    expect(approveActionRequest).not.toHaveBeenCalled();
    expect(enqueueActionExecute).not.toHaveBeenCalled();
  });

  it("reject on a pending request rejects it", async () => {
    vi.mocked(getActionRequest).mockResolvedValue({ id: "ar-1", status: "pending_approval", kind: "k" } as never);
    await dispatchInboundIntent("org-1", { kind: "decision", decisionRef: "ar-1", choice: "reject", reason: "nope" });
    expect(rejectActionRequest).toHaveBeenCalledWith({
      id: "ar-1",
      orgId: "org-1",
      approverUserId: null,
      reason: "nope",
      approver: { userId: null, role: "member" },
    });
    expect(enqueueActionExecute).not.toHaveBeenCalled();
  });

  it("a decision for an unknown action_request is a no-op", async () => {
    vi.mocked(getActionRequest).mockResolvedValue(undefined as never);
    await dispatchInboundIntent("org-1", { kind: "decision", decisionRef: "ghost", choice: "approve" });
    expect(approveActionRequest).not.toHaveBeenCalled();
    expect(rejectActionRequest).not.toHaveBeenCalled();
  });
});

describe("dispatchInboundIntent — utterance", () => {
  it("starts a chat run (thread + WORK_RUN enqueue) carrying the channel + recipient", async () => {
    await dispatchInboundIntent(
      "org-1",
      { kind: "utterance", text: "how are sales?", threadRef: "42" },
      "telegram",
      { chatId: 7 },
    );
    expect(getOrCreateChannelThread).toHaveBeenCalled();
    expect(enqueueWorkRun).toHaveBeenCalledWith(
      expect.objectContaining({
        orgId: "org-1",
        message: "how are sales?",
        runId: "run-1",
        threadId: "thread-1",
        channel: "telegram",
        channelPlugin: "telegram",
        recipient: { chatId: 7 },
      }),
    );
  });
});

describe("dispatchInboundIntent — spend budget", () => {
  it("replies with the budget message and starts no run when a budget is full", async () => {
    const { SpendBudgetExceeded } = await import("@neko/llm/spend");
    vi.mocked(createWorkRun).mockRejectedValueOnce(
      new SpendBudgetExceeded("org_daily", 500, 499, 5, 3600, new Date("2026-09-18T00:00:00Z")),
    );
    await dispatchInboundIntent(
      "org-1",
      { kind: "utterance", text: "how are sales?", threadRef: "42" },
      "telegram",
      { chatId: 7 },
    );
    expect(enqueueWorkRun).not.toHaveBeenCalled();
    expect(enqueueChannelDeliver).toHaveBeenCalledWith(
      expect.objectContaining({
        channelPlugin: "telegram",
        recipient: { chatId: 7 },
        events: [
          expect.objectContaining({
            kind: "converse",
            text: "The daily spending limit of $500.00 for this organization is reached. Try again after 00:00 UTC.",
          }),
        ],
      }),
      expect.anything(),
    );
  });
});

describe("dispatchInboundIntent — select (continuation fallback)", () => {
  it("continues the conversation with the chosen option", async () => {
    await dispatchInboundIntent(
      "org-1",
      { kind: "select", ref: "ar-9", optionId: "Option B" },
      "telegram",
      { chatId: 7 },
    );
    expect(getOrCreateChannelThread).toHaveBeenCalled();
    expect(enqueueWorkRun).toHaveBeenCalledWith(
      expect.objectContaining({
        message: "Option B",
        channelPlugin: "telegram",
      }),
    );
  });
});

describe("dispatchInboundIntent — channel thread-reply gate (W3.2)", () => {
  it("drops a requireThread reply when no matching thread exists", async () => {
    vi.mocked(getWorkThread).mockResolvedValueOnce(null);
    await dispatchInboundIntent(
      "org-1",
      { kind: "utterance", text: "and last quarter?", threadRef: "171.5" },
      "slack",
      { kind: "slack", channel: "C9", thread_ts: "171.5", requireThread: true },
    );
    expect(getOrCreateChannelThread).not.toHaveBeenCalled();
    expect(enqueueWorkRun).not.toHaveBeenCalled();
  });

  it("continues a requireThread reply when the thread already exists", async () => {
    vi.mocked(getWorkThread).mockResolvedValueOnce({ id: "thread-1" } as never);
    await dispatchInboundIntent(
      "org-1",
      { kind: "utterance", text: "and last quarter?", threadRef: "171.5" },
      "slack",
      { kind: "slack", channel: "C9", thread_ts: "171.5", requireThread: true },
    );
    expect(getOrCreateChannelThread).toHaveBeenCalled();
    expect(enqueueWorkRun).toHaveBeenCalledWith(
      expect.objectContaining({ message: "and last quarter?" }),
    );
  });
});

describe("conversationKeyFor", () => {
  it("keys a Slack DM by channel and a thread by channel:threadRef", () => {
    expect(conversationKeyFor({ kind: "slack", channel: "D9" })).toBe("D9");
    expect(conversationKeyFor({ kind: "slack", channel: "C9" }, "171.5")).toBe("C9:171.5");
  });

  it("keys Telegram by chatId and falls back to recipient address", () => {
    expect(conversationKeyFor({ chatId: 7 }, "42")).toBe("7:42");
    expect(conversationKeyFor({ to: "+15550001111" })).toBe("+15550001111");
  });

  it("returns empty when the recipient has no stable address", () => {
    expect(conversationKeyFor({})).toBe("");
    expect(conversationKeyFor({ foo: "bar" }, "42")).toBe("");
  });
});

describe("deliverChatReply (channel reply path)", () => {
  it("queues a durable converse delivery keyed by reply id", async () => {
    await deliverChatReply("org-1", "telegram", { chatId: 7 }, "run-9", "We have 290 employees.");
    expect(enqueueChannelDeliver).toHaveBeenCalledTimes(1);
    const [payload, key] = vi.mocked(enqueueChannelDeliver).mock.calls[0]!;
    expect(payload).toMatchObject({
      orgId: "org-1",
      channelPlugin: "telegram",
      recipient: { chatId: 7 },
      events: [{ kind: "converse", id: "run-9", role: "assistant", text: "We have 290 employees." }],
    });
    expect(key).toBe("reply-run-9");
  });

  it("does not enqueue an empty reply", async () => {
    await deliverChatReply("org-1", "p", {}, "run-9", "   ");
    expect(enqueueChannelDeliver).not.toHaveBeenCalled();
  });
});

describe("ingestInboundWebhook", () => {
  const fakeReg = {
    verifyInbound: vi.mocked(verifyChannelInbound),
    parseInbound: vi.mocked(parseChannelInbound),
  };

  beforeEach(() => {
    fakeReg.verifyInbound.mockResolvedValue(true);
    fakeReg.parseInbound.mockResolvedValue({
      intents: [{ kind: "decision", decisionRef: "ar-1", choice: "approve" }],
    });
    vi.mocked(getActionRequest).mockResolvedValue({ id: "ar-1", status: "pending_approval", kind: "k" } as never);
  });

  it("verifies, parses, dispatches, and marks done when the signature is valid", async () => {
    const res = await ingestInboundWebhook("org-1", "telegram", { "x-secret": "s" }, '{"callback_query":{}}');
    expect(res).toEqual({ ok: true, dispatched: 1 });
    expect(fakeReg.verifyInbound).toHaveBeenCalled();
    expect(fakeReg.parseInbound).toHaveBeenCalled();
    expect(approveActionRequest).toHaveBeenCalled();
    expect(markInboundDone).toHaveBeenCalled();
  });

  it("DEDUPES a duplicate update — begin says skip ⇒ no parse, no dispatch, still ok", async () => {
    vi.mocked(beginInboundUpdate).mockResolvedValue({ proceed: false, attempts: 1, dead: false });
    const res = await ingestInboundWebhook("org-1", "telegram", { "x-secret": "s" }, '{"callback_query":{}}');
    expect(res).toEqual({ ok: true, dispatched: 1 });
    expect(fakeReg.parseInbound).not.toHaveBeenCalled();
    expect(approveActionRequest).not.toHaveBeenCalled();
  });

  it("SKIPS a dead-lettered update — never re-dispatches a poison message", async () => {
    vi.mocked(beginInboundUpdate).mockResolvedValue({ proceed: false, attempts: 30, dead: true });
    const res = await ingestInboundWebhook("org-1", "telegram", { "x-secret": "s" }, '{"callback_query":{}}');
    expect(res).toEqual({ ok: true, dispatched: 1 });
    expect(fakeReg.parseInbound).not.toHaveBeenCalled();
  });

  it("records the failure + reports ok:false while still retrying (provider can redeliver)", async () => {
    fakeReg.parseInbound.mockRejectedValue(new Error("VM down"));
    vi.mocked(recordInboundFailure).mockResolvedValue({ dead: false, attempts: 3 });
    const res = await ingestInboundWebhook("org-1", "telegram", { "x-secret": "s" }, '{"callback_query":{}}');
    expect(res).toEqual({ ok: false, dispatched: 0 });
    expect(recordInboundFailure).toHaveBeenCalledWith(
      "org-1",
      "telegram",
      "update-key",
      30,
      expect.anything(),
      "VM down",
    );
  });

  it("DEAD-LETTERS after the attempt cap ⇒ consumed (ok:true), not retried again", async () => {
    fakeReg.parseInbound.mockRejectedValue(new Error("still broken"));
    vi.mocked(recordInboundFailure).mockResolvedValue({ dead: true, attempts: 30 });
    const res = await ingestInboundWebhook("org-1", "telegram", { "x-secret": "s" }, '{"callback_query":{}}');
    expect(res).toEqual({ ok: true, dispatched: 1 });
  });

  it("refuses (no parse/dispatch) when verification fails", async () => {
    fakeReg.verifyInbound.mockResolvedValue(false);
    const res = await ingestInboundWebhook("org-1", "telegram", {}, "{}");
    expect(res).toEqual({ ok: false, dispatched: 0 });
    expect(fakeReg.parseInbound).not.toHaveBeenCalled();
    expect(approveActionRequest).not.toHaveBeenCalled();
  });

  it("returns ok:false for an unknown channel without verifying", async () => {
    const res = await ingestInboundWebhook("org-1", "discord", {}, "{}");
    expect(res).toEqual({ ok: false, dispatched: 0 });
    expect(fakeReg.verifyInbound).not.toHaveBeenCalled();
  });

  it("accepts the legacy plugin package name in the webhook path", async () => {
    const res = await ingestInboundWebhook("org-1", "@open-neko/channel-telegram", { "x-secret": "s" }, '{"callback_query":{}}');
    expect(res).toEqual({ ok: true, dispatched: 1 });
    expect(fakeReg.verifyInbound).toHaveBeenCalledWith("telegram", { "x-secret": "s" }, '{"callback_query":{}}');
    expect(fakeReg.parseInbound).toHaveBeenCalledWith("telegram", { callback_query: {} });
  });
});

describe("ensureInboundBinding (auto-bind on first inbound)", () => {
  beforeEach(() => {
    h.binds.length = 0;
    h.selectRows = [];
  });

  it("writes a binding when none exists for (org, channel)", async () => {
    await ensureInboundBinding("org-1", "telegram", { kind: "telegram", chatId: 99 });
    expect(h.binds).toHaveLength(1);
    expect(h.binds[0]).toMatchObject({
      org_id: "org-1",
      channel_plugin: "telegram",
      recipient: { kind: "telegram", chatId: 99 },
      enabled: true,
    });
  });

  it("is idempotent — skips writing when a binding already exists", async () => {
    h.selectRows = [{ id: "b1" }];
    await ensureInboundBinding("org-1", "telegram", { kind: "telegram", chatId: 99 });
    expect(h.binds).toHaveLength(0);
  });
});
