import { describe, expect, it } from "vitest";
import {
  buildSubscriptionQuery,
  parseWorkflowOutputMatch,
} from "../src/workflows/subscription-query";

describe("buildSubscriptionQuery (workflow_output)", () => {
  it("scopes by org_id and translates scope/mood/topic/kinds", () => {
    const payload = buildSubscriptionQuery({
      sourceKind: "workflow_output",
      orgId: "org-abc",
      filter: {
        scope: "apac_churn",
        topic: "partner_renewal",
        mood: ["watch", "act"],
        kinds: ["finding", "recommendation"],
      },
    });
    expect(payload).not.toBeNull();
    expect(payload!.variables).toEqual({
      where: {
        org_id: { eq: "org-abc" },
        scope: { eq: "apac_churn" },
        topic: { eq: "partner_renewal" },
        mood: { in: ["watch", "act"] },
        kind: { in: ["finding", "recommendation"] },
      },
    });
    expect(payload!.query).toContain("subscription WorkflowOutputMatch");
    expect(payload!.query).toContain("workflow_output");
  });

  it("returns null for source_kinds not yet wired", () => {
    expect(
      buildSubscriptionQuery({
        sourceKind: "source_change",
        orgId: "org-abc",
        filter: {},
      }),
    ).toBeNull();
    expect(
      buildSubscriptionQuery({
        sourceKind: "external_event",
        orgId: "org-abc",
        filter: {},
      }),
    ).toBeNull();
  });
});

describe("parseWorkflowOutputMatch", () => {
  it("extracts the first workflow_output row from a subscription payload", () => {
    const match = parseWorkflowOutputMatch({
      data: {
        workflow_output: [
          {
            id: "out-1",
            org_id: "org-abc",
            workflow_run_id: "wfr-1",
            kind: "finding",
            scope: "apac_churn",
            topic: null,
            mood: "watch",
            title: "Churn spike",
            created_at: "2026-05-13T12:00:00.000Z",
          },
        ],
      },
    });
    expect(match?.id).toBe("out-1");
    expect(match?.mood).toBe("watch");
    expect(match?.topic).toBeNull();
  });

  it("returns null on empty payload", () => {
    expect(parseWorkflowOutputMatch(null)).toBeNull();
    expect(
      parseWorkflowOutputMatch({ data: { workflow_output: [] } }),
    ).toBeNull();
  });
});
