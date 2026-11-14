import { beforeEach, describe, expect, it } from "vitest";
import {
  cancelOfflineQuestion,
  claimNextOfflineQuestion,
  clearOfflineQueueForTests,
  completeOfflineQuestion,
  enqueueOfflineQuestion,
  getOfflineQueueSnapshot,
  requeueOfflineQuestion,
} from "../src/app/(work)/work/work-offline-queue";
import {
  applyStreamEventToBundle,
  withAssistantTimelinePlaceholders,
} from "../src/app/(work)/work/work-thread";
import type { ThreadBundle } from "../src/app/(work)/work/work-types";

describe("stream resume and offline question queue (4.6)", () => {
  beforeEach(() => {
    clearOfflineQueueForTests();
  });

  it("preserves partial streamed assistant text when resuming an in-flight run on reload", () => {
    const initialBundle: ThreadBundle = {
      thread: {
        id: "thread-1",
        title: "Inventory check",
        createdAt: "2026-11-14T10:00:00.000Z",
        updatedAt: "2026-11-14T10:00:05.000Z",
        lastMessageAt: "2026-11-14T10:00:05.000Z",
      },
      runs: [
        {
          id: "run-1",
          backend: "ax",
          status: "running",
          error: null,
          createdAt: "2026-11-14T10:00:01.000Z",
          finishedAt: null,
          analysisMinutesSaved: null,
          analysisMinutesBasis: null,
          actorRole: "admin",
        },
      ],
      messages: [
        {
          id: "msg-user-1",
          runId: "run-1",
          role: "user",
          content: "Summarize warehouse cover",
          createdAt: "2026-11-14T10:00:01.000Z",
        },
      ],
      eventsByRun: {
        "run-1": [
          { type: "status", message: "Agent is working…" },
          {
            type: "message",
            role: "assistant",
            content: "North warehouse has 18.4 days of cover, ",
          },
        ],
      },
    };

    const hydrated = withAssistantTimelinePlaceholders(initialBundle);
    const assistantPlaceholder = hydrated.messages.find(
      (m) => m.role === "assistant" && m.runId === "run-1",
    );
    expect(assistantPlaceholder?.content).toBe(
      "North warehouse has 18.4 days of cover, ",
    );
    expect(hydrated.eventsByRun["run-1"]).toHaveLength(2);

    const resumed = applyStreamEventToBundle(hydrated, "run-1", {
      type: "message",
      role: "assistant",
      content: "while South warehouse is down to 0.1 days.",
    });
    const completed = applyStreamEventToBundle(resumed, "run-1", {
      type: "done",
      result: { status: "completed" },
    });

    const finalAssistant = completed.messages.find(
      (m) => m.role === "assistant" && m.runId === "run-1",
    );
    expect(finalAssistant?.content).toBe(
      "North warehouse has 18.4 days of cover, while South warehouse is down to 0.1 days.",
    );
    expect(completed.runs[0]?.status).toBe("completed");
  });

  it("queues offline questions, allows cancelling, and claims each remaining question once", () => {
    const q1 = enqueueOfflineQuestion({
      id: "offline-1",
      threadId: "thread-1",
      threadBase: "/chat/t",
      content: "Cancel this question while offline",
    });
    const q2 = enqueueOfflineQuestion({
      id: "offline-2",
      threadId: "thread-1",
      threadBase: "/chat/t",
      content: "Send this question once online",
    });

    expect(getOfflineQueueSnapshot().map((item) => item.id)).toEqual([
      q1.id,
      q2.id,
    ]);

    expect(cancelOfflineQuestion(q1.id)).toBe(true);
    expect(cancelOfflineQuestion(q1.id)).toBe(false);
    expect(getOfflineQueueSnapshot().map((item) => item.id)).toEqual([q2.id]);

    const claimed = claimNextOfflineQuestion("thread-1", "/chat/t");
    expect(claimed?.id).toBe(q2.id);
    expect(claimed?.status).toBe("sending");

    // A second claim while the first is in flight returns null so the question sends only once.
    expect(claimNextOfflineQuestion("thread-1", "/chat/t")).toBeNull();

    // If the network drops again before the POST succeeds, requeue restores waiting status.
    requeueOfflineQuestion(claimed!);
    expect(getOfflineQueueSnapshot()[0]?.status).toBe("waiting");

    const claimedAgain = claimNextOfflineQuestion("thread-1", "/chat/t");
    expect(claimedAgain?.id).toBe(q2.id);
    completeOfflineQuestion(q2.id);
    expect(getOfflineQueueSnapshot()).toEqual([]);
  });
});
