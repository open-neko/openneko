import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

// delivery.ts wires several cross-package seams; mock them so the dispatch
// logic (idempotency + routing) is tested in isolation.
vi.mock("@neko/llm/workflows", () => ({
  getActionRequest: vi.fn(),
  approveActionRequest: vi.fn(async () => ({})),
  rejectActionRequest: vi.fn(async () => ({})),
  setWorkflowOutputDeliveryHook: vi.fn(),
}));
vi.mock("@neko/db/jobs", () => ({
  enqueue: vi.fn(async () => "job-1"),
  QUEUE: { ACTION_EXECUTE: "action_execute", WORK_RUN: "work_run" },
}));
vi.mock("@neko/db", () => ({
  db: vi.fn(() => ({
    insert: () => ({ values: () => ({ returning: async () => [{ id: "pj-1" }] }) }),
    select: () => ({ from: () => ({ where: async () => [] }) }),
  })),
  and: (...a: unknown[]) => a,
  eq: (...a: unknown[]) => a,
  delivery_binding: { org_id: {}, enabled: {} },
  processing_job: { id: {} },
}));
vi.mock("@neko/llm", () => ({ resolveAgentBackend: vi.fn(async () => ({ id: "hermes" })) }));
vi.mock("@neko/llm/work", () => ({
  createWorkThread: vi.fn(async () => ({ id: "thread-1" })),
  createWorkRun: vi.fn(async () => ({ id: "run-1" })),
}));
vi.mock("@neko/llm/interaction", () => ({
  outputRowToInteractionEvent: (r: unknown) => ({ kind: "inform", ...(r as object) }),
}));
vi.mock("../../src/plugins/registry-instance.js", () => ({
  getPluginRegistryInstance: vi.fn(),
}));

import { enqueue } from "@neko/db/jobs";
import {
  approveActionRequest,
  getActionRequest,
  rejectActionRequest,
} from "@neko/llm/workflows";
import { createWorkThread } from "@neko/llm/work";
import { dispatchInboundIntent, ingestInboundWebhook } from "../../src/channels/delivery";
import { getPluginRegistryInstance } from "../../src/plugins/registry-instance.js";

beforeEach(() => vi.clearAllMocks());
afterEach(() => vi.clearAllMocks());

describe("dispatchInboundIntent — decision", () => {
  it("approve on a pending request approves + enqueues ACTION_EXECUTE", async () => {
    vi.mocked(getActionRequest).mockResolvedValue({ id: "ar-1", status: "pending_approval", kind: "k" } as never);
    await dispatchInboundIntent("org-1", { kind: "decision", decisionRef: "ar-1", choice: "approve" });
    expect(approveActionRequest).toHaveBeenCalledWith({ id: "ar-1", orgId: "org-1", approverUserId: null });
    expect(enqueue).toHaveBeenCalledWith("action_execute", { orgId: "org-1", actionRequestId: "ar-1" });
  });

  it("is idempotent — an already-executed request is NOT re-approved or re-enqueued", async () => {
    vi.mocked(getActionRequest).mockResolvedValue({ id: "ar-1", status: "executed", kind: "k" } as never);
    await dispatchInboundIntent("org-1", { kind: "decision", decisionRef: "ar-1", choice: "approve" });
    expect(approveActionRequest).not.toHaveBeenCalled();
    expect(enqueue).not.toHaveBeenCalled();
  });

  it("reject on a pending request rejects it", async () => {
    vi.mocked(getActionRequest).mockResolvedValue({ id: "ar-1", status: "pending_approval", kind: "k" } as never);
    await dispatchInboundIntent("org-1", { kind: "decision", decisionRef: "ar-1", choice: "reject", reason: "nope" });
    expect(rejectActionRequest).toHaveBeenCalledWith({ id: "ar-1", orgId: "org-1", approverUserId: null, reason: "nope" });
    expect(enqueue).not.toHaveBeenCalled();
  });

  it("a decision for an unknown action_request is a no-op", async () => {
    vi.mocked(getActionRequest).mockResolvedValue(undefined as never);
    await dispatchInboundIntent("org-1", { kind: "decision", decisionRef: "ghost", choice: "approve" });
    expect(approveActionRequest).not.toHaveBeenCalled();
    expect(rejectActionRequest).not.toHaveBeenCalled();
  });
});

describe("dispatchInboundIntent — utterance", () => {
  it("starts a chat run (thread + WORK_RUN enqueue)", async () => {
    await dispatchInboundIntent("org-1", { kind: "utterance", text: "how are sales?", threadRef: "42" });
    expect(createWorkThread).toHaveBeenCalled();
    expect(enqueue).toHaveBeenCalledWith(
      "work_run",
      expect.objectContaining({ orgId: "org-1", message: "how are sales?", runId: "run-1", threadId: "thread-1" }),
    );
  });
});

describe("ingestInboundWebhook", () => {
  const fakeReg = {
    verifyInbound: vi.fn(async () => true),
    parseInbound: vi.fn(async () => [{ kind: "decision", decisionRef: "ar-1", choice: "approve" }]),
  };

  beforeEach(() => {
    fakeReg.verifyInbound.mockResolvedValue(true);
    fakeReg.parseInbound.mockResolvedValue([{ kind: "decision", decisionRef: "ar-1", choice: "approve" }]);
    vi.mocked(getPluginRegistryInstance).mockReturnValue(fakeReg as never);
    vi.mocked(getActionRequest).mockResolvedValue({ id: "ar-1", status: "pending_approval", kind: "k" } as never);
  });

  it("verifies, parses, and dispatches when the signature is valid", async () => {
    const res = await ingestInboundWebhook("org-1", "@open-neko/channel-telegram", { "x-secret": "s" }, '{"callback_query":{}}');
    expect(res).toEqual({ ok: true, dispatched: 1 });
    expect(fakeReg.verifyInbound).toHaveBeenCalled();
    expect(fakeReg.parseInbound).toHaveBeenCalled();
    expect(approveActionRequest).toHaveBeenCalled();
  });

  it("refuses (no parse/dispatch) when verification fails", async () => {
    fakeReg.verifyInbound.mockResolvedValue(false);
    const res = await ingestInboundWebhook("org-1", "@open-neko/channel-telegram", {}, "{}");
    expect(res).toEqual({ ok: false, dispatched: 0 });
    expect(fakeReg.parseInbound).not.toHaveBeenCalled();
    expect(approveActionRequest).not.toHaveBeenCalled();
  });

  it("returns ok:false when no registry is available", async () => {
    vi.mocked(getPluginRegistryInstance).mockReturnValue(null);
    const res = await ingestInboundWebhook("org-1", "@open-neko/channel-telegram", {}, "{}");
    expect(res).toEqual({ ok: false, dispatched: 0 });
  });
});
