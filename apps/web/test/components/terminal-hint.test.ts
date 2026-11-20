import { describe, expect, it } from "vitest";
import { terminalHint } from "@/app/admin/settings/agent/terminal-hint";

describe("terminalHint", () => {
  it("names the missing sandbox", () => {
    expect(terminalHint(false, "ignored")).toContain("no sandbox service");
  });
  it("shows why the server blocks the terminal", () => {
    const blocked = "The terminal is off: the sandbox network has a route out.";
    expect(terminalHint(true, blocked)).toBe(blocked);
  });
  it("describes the terminal when nothing blocks it", () => {
    expect(terminalHint(true, "")).toContain("Lets the agent run commands");
  });
});
