import { describe, expect, it, vi } from "vitest";
import { NextRequest } from "next/server";

const state = vi.hoisted(() => ({
  notify: undefined as undefined | ((channel: string, payload: string) => void),
  status: "running" as "running" | "completed",
  events: [] as Array<{ id: number; event: { type: string; role?: string; content?: string } }>,
  closed: false,
}));

vi.mock("@/lib/db", () => ({ getOrgId: async () => "owner-org" }));
vi.mock("@/lib/work-thread-auth", () => ({
  getAuthorizedWorkThread: async () => ({ id: "thread" }),
}));
vi.mock("@/lib/work-store", () => ({
  getWorkRun: async () => ({ thread_id: "thread", backend: "harness", status: state.status }),
  getWorkRunEventsAfter: async (_orgId: string, _runId: string, afterId: number) =>
    state.events.filter(({ id }) => id > afterId),
}));
vi.mock("@/lib/neko-run-registry", () => ({ subscribeToRun: () => () => {} }));
vi.mock("@neko/db", () => ({
  createNotifyClient: async () => ({
    on: (handler: typeof state.notify) => { state.notify = handler; },
    close: async () => { state.closed = true; },
  }),
}));

import { GET } from "@/app/api/work/threads/[threadId]/runs/[runId]/events/route";

async function nextFrame(reader: ReadableStreamDefaultReader<Uint8Array>): Promise<string> {
  const read = reader.read();
  const timeout = new Promise<never>((_resolve, reject) =>
    setTimeout(() => reject(new Error("SSE frame timeout")), 2_000));
  const result = await Promise.race([read, timeout]);
  if (result.done) throw new Error("SSE stream closed early");
  return new TextDecoder().decode(result.value);
}

describe("work run provisional SSE", () => {
  it("delivers scoped live text without advancing the durable replay cursor", async () => {
    state.notify = undefined;
    state.status = "running";
    state.events = [];
    state.closed = false;
    const response = await GET(new NextRequest("http://localhost/api/work/threads/thread/runs/run/events?afterId=7"), {
      params: Promise.resolve({ threadId: "thread", runId: "run" }),
    });
    expect(response.status).toBe(200);
    const reader = response.body!.getReader();
    try {
      expect(await nextFrame(reader)).toContain(": hello");
      expect(await nextFrame(reader)).toContain('"type":"hello"');
      expect(state.notify).toBeTypeOf("function");

      state.notify!("work_run_progress", JSON.stringify({
        orgId: "other-org", runId: "run",
        event: { type: "provisional_answer", version: 0, index: 0, text: "hidden" },
      }));
      state.notify!("work_run_progress", JSON.stringify({
        orgId: "owner-org", runId: "other-run",
        event: { type: "provisional_answer", version: 0, index: 0, text: "hidden" },
      }));
      state.notify!("work_run_progress", JSON.stringify({
        orgId: "owner-org", runId: "run",
        event: { type: "provisional_answer", version: 0, index: 0, text: "draft" },
      }));
      const preview = await nextFrame(reader);
      expect(preview).toContain('"text":"draft"');
      expect(preview).not.toContain("id:");

      state.events = [{ id: 8, event: { type: "message", role: "assistant", content: "verified" } }];
      state.status = "completed";
      state.notify!("work_run_event", "run");
      const canonical = await nextFrame(reader);
      expect(canonical).toContain("id: 8\n");
      expect(canonical).toContain('"content":"verified"');
      expect((await reader.read()).done).toBe(true);
      expect(state.closed).toBe(true);
    } finally {
      await reader.cancel().catch(() => {});
    }
  });
});
