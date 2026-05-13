import { describe, expect, it } from "vitest";
import {
  checkSubscriptionWouldLoop,
  outputMatchesFilter,
  SubscriptionSelfLoopError,
} from "../src/workflows/cycle-detection";

describe("outputMatchesFilter", () => {
  const baseOutput = {
    kind: "finding",
    scope: "apac_churn",
    topic: "partner_renewal",
    mood: "watch",
  };

  it("matches when filter is empty", () => {
    expect(outputMatchesFilter(baseOutput, {})).toBe(true);
  });

  it("scope eq filter", () => {
    expect(outputMatchesFilter(baseOutput, { scope: "apac_churn" })).toBe(true);
    expect(outputMatchesFilter(baseOutput, { scope: "other" })).toBe(false);
  });

  it("topic eq filter", () => {
    expect(
      outputMatchesFilter(baseOutput, { topic: "partner_renewal" }),
    ).toBe(true);
    expect(outputMatchesFilter(baseOutput, { topic: "other" })).toBe(false);
  });

  it("mood scalar + array filter", () => {
    expect(outputMatchesFilter(baseOutput, { mood: "watch" })).toBe(true);
    expect(
      outputMatchesFilter(baseOutput, { mood: ["watch", "act"] }),
    ).toBe(true);
    expect(outputMatchesFilter(baseOutput, { mood: ["good"] })).toBe(false);
  });

  it("kinds array filter", () => {
    expect(
      outputMatchesFilter(baseOutput, { kinds: ["finding", "observation"] }),
    ).toBe(true);
    expect(outputMatchesFilter(baseOutput, { kinds: ["recommendation"] })).toBe(
      false,
    );
  });

  it("null fields don't match when filter requires them", () => {
    expect(
      outputMatchesFilter(
        { ...baseOutput, mood: null },
        { mood: ["watch"] },
      ),
    ).toBe(false);
  });
});

describe("checkSubscriptionWouldLoop", () => {
  it("rejects when any recent output matches the proposed filter", async () => {
    await expect(
      checkSubscriptionWouldLoop({
        orgId: "org-1",
        workflowId: "wf-1",
        filter: { scope: "apac_churn" },
        listRecentOutputs: async () => [
          {
            id: "out-old",
            kind: "finding",
            scope: "apac_churn",
            topic: null,
            mood: "watch",
            createdAt: new Date(),
          },
        ],
      }),
    ).rejects.toBeInstanceOf(SubscriptionSelfLoopError);
  });

  it("allows when no recent output matches", async () => {
    await expect(
      checkSubscriptionWouldLoop({
        orgId: "org-1",
        workflowId: "wf-1",
        filter: { scope: "other_scope" },
        listRecentOutputs: async () => [
          {
            id: "out-1",
            kind: "finding",
            scope: "apac_churn",
            topic: null,
            mood: "watch",
            createdAt: new Date(),
          },
        ],
      }),
    ).resolves.toBeUndefined();
  });

  it("error carries the matching output ids for UI feedback", async () => {
    try {
      await checkSubscriptionWouldLoop({
        orgId: "org-1",
        workflowId: "wf-1",
        filter: { mood: "watch" },
        listRecentOutputs: async () => [
          {
            id: "out-a",
            kind: "finding",
            scope: "x",
            topic: null,
            mood: "watch",
            createdAt: new Date(),
          },
          {
            id: "out-b",
            kind: "finding",
            scope: "y",
            topic: null,
            mood: "watch",
            createdAt: new Date(),
          },
        ],
      });
      expect.fail("expected error");
    } catch (e) {
      expect(e).toBeInstanceOf(SubscriptionSelfLoopError);
      const err = e as SubscriptionSelfLoopError;
      expect(err.matchingOutputIds).toEqual(["out-a", "out-b"]);
    }
  });
});
