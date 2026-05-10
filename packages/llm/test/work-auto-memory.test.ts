import { describe, expect, it } from "vitest";
import {
  decideWorkMemoryDraftDisposition,
  hasExplicitMemorySignal,
  type WorkMemoryDraft,
} from "../src/work/auto-memory";

const draft: WorkMemoryDraft = {
  text: "Revenue should be reported in INR lakhs.",
  kind: "preference",
  scope: "global",
  confidence: 0.88,
};

describe("work auto-memory prompt gating", () => {
  it("detects explicit durable memory language", () => {
    expect(hasExplicitMemorySignal("Remember that revenue is in INR lakhs.")).toBe(true);
    expect(hasExplicitMemorySignal("Show me revenue by month for 2025.")).toBe(false);
  });

  it("skips medium-confidence inferred drafts in default mode", () => {
    expect(
      decideWorkMemoryDraftDisposition({
        draft,
        mode: "on",
        conflictCount: 0,
        userMessage: "Show me revenue by month for 2025.",
      }),
    ).toBe("skip");
  });

  it("prompts only when the user made the durable intent explicit", () => {
    expect(
      decideWorkMemoryDraftDisposition({
        draft,
        mode: "on",
        conflictCount: 1,
        userMessage: "Going forward, revenue should be reported in INR lakhs.",
      }),
    ).toBe("pending");
  });

  it("auto-saves high-confidence explicit drafts without conflicts", () => {
    expect(
      decideWorkMemoryDraftDisposition({
        draft: { ...draft, confidence: 0.94 },
        mode: "on",
        conflictCount: 0,
        userMessage: "Always report revenue in INR lakhs.",
      }),
    ).toBe("save");
  });
});
