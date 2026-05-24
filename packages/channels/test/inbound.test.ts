import { describe, expect, it } from "vitest";
import { intentFromButtonId, parseSlackInbound, parseWhatsappInbound } from "../src/index.js";

describe("parseSlackInbound", () => {
  it("maps an Approve tap to a decision", () => {
    const raw = { type: "block_actions", actions: [{ action_id: "approve", value: "ar-123" }] };
    expect(parseSlackInbound(raw)).toEqual([{ kind: "decision", decisionRef: "ar-123", choice: "approve" }]);
  });

  it("maps a Reject tap to a decision", () => {
    const raw = { type: "block_actions", actions: [{ action_id: "reject", value: "ar-9" }] };
    expect(parseSlackInbound(raw)).toEqual([{ kind: "decision", decisionRef: "ar-9", choice: "reject" }]);
  });

  it("maps an option button to a select", () => {
    const raw = { type: "block_actions", actions: [{ action_id: "select:opt-a", value: "ar-7:opt-a" }] };
    expect(parseSlackInbound(raw)).toEqual([{ kind: "select", ref: "ar-7", optionId: "opt-a" }]);
  });

  it("maps a human message to an utterance carrying the thread", () => {
    const raw = { type: "event_callback", event: { type: "message", text: "what's our churn?", thread_ts: "171.99" } };
    expect(parseSlackInbound(raw)).toEqual([{ kind: "utterance", text: "what's our churn?", threadRef: "171.99" }]);
  });

  it("ignores the bot's own echoes", () => {
    const raw = { type: "event_callback", event: { type: "message", text: "hi", bot_id: "B1" } };
    expect(parseSlackInbound(raw)).toEqual([]);
  });

  it("returns nothing for unrelated payloads", () => {
    expect(parseSlackInbound({ type: "url_verification" })).toEqual([]);
    expect(parseSlackInbound(null)).toEqual([]);
  });
});

describe("parseWhatsappInbound", () => {
  const envelope = (message: unknown) => ({
    entry: [{ changes: [{ value: { messages: [message] } }] }],
  });

  it("maps a text message to an utterance", () => {
    expect(parseWhatsappInbound(envelope({ type: "text", text: { body: "approve it" } }))).toEqual([
      { kind: "utterance", text: "approve it" },
    ]);
  });

  it("maps an interactive button reply to a decision", () => {
    const raw = envelope({ type: "interactive", interactive: { button_reply: { id: "approve:ar-55" } } });
    expect(parseWhatsappInbound(raw)).toEqual([{ kind: "decision", decisionRef: "ar-55", choice: "approve" }]);
  });

  it("returns nothing for an empty webhook", () => {
    expect(parseWhatsappInbound({ entry: [] })).toEqual([]);
  });
});

describe("intentFromButtonId", () => {
  it("decodes the select convention", () => {
    expect(intentFromButtonId("select:ar-1:opt-a")).toEqual({ kind: "select", ref: "ar-1", optionId: "opt-a" });
  });

  it("rejects ids without a verb", () => {
    expect(intentFromButtonId("garbage")).toBeNull();
  });
});
