import { describe, expect, it } from "vitest";
import { withRunEventFormat } from "@/lib/run-event-format";

describe("withRunEventFormat", () => {
  it("adds the format to a URL with or without a query", () => {
    expect(withRunEventFormat("/api/v1/work/threads/t1")).toBe("/api/v1/work/threads/t1?events=2");
    expect(withRunEventFormat("/api/v1/work/threads/t1/runs/r1/events?afterId=7")).toBe(
      "/api/v1/work/threads/t1/runs/r1/events?afterId=7&events=2",
    );
  });
});
