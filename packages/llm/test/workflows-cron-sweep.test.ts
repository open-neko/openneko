import { describe, expect, it } from "vitest";
import {
  computeDueWorkflows,
  singletonKeyForFiring,
} from "../src/workflows/cron-sweep";
import type { WorkflowRecord } from "../src/workflows/store";

function fakeWorkflow(
  overrides: Partial<WorkflowRecord> & { cron?: string },
): WorkflowRecord {
  return {
    id: overrides.id ?? "wf-1",
    orgId: overrides.orgId ?? "org-1",
    name: overrides.name ?? "Sweep test",
    description: "",
    enabled: overrides.enabled ?? true,
    status: "active",
    goal: "",
    systemPromptOverlay: "",
    steps: [],
    cron: overrides.cron ?? null,
    cronTimezone: overrides.cronTimezone ?? "UTC",
    cronEnabled: overrides.cronEnabled ?? true,
    dailyRunBudget: overrides.dailyRunBudget ?? null,
    outputContract: null,
    createdByThreadId: null,
    createdByRunId: null,
    createdAt: new Date(),
    updatedAt: new Date(),
  };
}

describe("computeDueWorkflows", () => {
  it("returns a workflow whose cron fires inside the window", async () => {
    const due = await computeDueWorkflows({
      windowStart: new Date("2026-05-13T11:59:30Z"),
      windowEnd: new Date("2026-05-13T12:00:30Z"),
      workflows: [fakeWorkflow({ cron: "0 12 * * *", cronTimezone: "UTC" })],
    });
    expect(due).toHaveLength(1);
    expect(due[0].firingTime.toISOString()).toBe("2026-05-13T12:00:00.000Z");
  });

  it("skips workflows whose cron does not fire in the window", async () => {
    const due = await computeDueWorkflows({
      windowStart: new Date("2026-05-13T12:01:00Z"),
      windowEnd: new Date("2026-05-13T12:02:00Z"),
      workflows: [fakeWorkflow({ cron: "0 12 * * *", cronTimezone: "UTC" })],
    });
    expect(due).toHaveLength(0);
  });

  it("skips disabled workflows and disabled crons", async () => {
    const due = await computeDueWorkflows({
      windowStart: new Date("2026-05-13T11:59:30Z"),
      windowEnd: new Date("2026-05-13T12:00:30Z"),
      workflows: [
        fakeWorkflow({
          id: "wf-disabled",
          enabled: false,
          cron: "0 12 * * *",
        }),
        fakeWorkflow({
          id: "wf-cron-off",
          cronEnabled: false,
          cron: "0 12 * * *",
        }),
      ],
    });
    expect(due).toHaveLength(0);
  });

  it("tolerates invalid cron expressions without throwing", async () => {
    const due = await computeDueWorkflows({
      windowStart: new Date("2026-05-13T11:00:00Z"),
      windowEnd: new Date("2026-05-13T13:00:00Z"),
      workflows: [
        fakeWorkflow({ id: "wf-bad", cron: "not a real cron" }),
        fakeWorkflow({ id: "wf-ok", cron: "0 12 * * *" }),
      ],
    });
    expect(due.map((d) => d.workflow.id)).toEqual(["wf-ok"]);
  });
});

describe("singletonKeyForFiring", () => {
  it("encodes workflow id and ISO firing time", () => {
    const key = singletonKeyForFiring(
      "wf-abc",
      new Date("2026-05-13T12:00:00Z"),
    );
    expect(key).toBe("wf-abc:2026-05-13T12:00:00.000Z");
  });

  it("differs across firing times so the dedup window stays per-fire", () => {
    const a = singletonKeyForFiring("wf-1", new Date("2026-05-13T12:00:00Z"));
    const b = singletonKeyForFiring("wf-1", new Date("2026-05-13T13:00:00Z"));
    expect(a).not.toBe(b);
  });
});
