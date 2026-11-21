import { describe, expect, it } from "vitest";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import UnconfinedTurnsField, { type UnconfinedTurnsState } from "@/app/admin/settings/agent/UnconfinedTurnsField";

function render(initial: UnconfinedTurnsState) {
  return renderToStaticMarkup(createElement(UnconfinedTurnsField, { initial }));
}

describe("UnconfinedTurnsField", () => {
  it("offers nothing on a system that can confine", () => {
    const html = render({
      setting: { on: false },
      system: { available: false, blocked: "This system confines agent turns, so this setting stays off." },
    });
    expect(html).toContain("This system confines agent turns");
    expect(html).not.toContain("Run agent turns without confinement");
    expect(html).not.toContain("agent-unconfined-confirm");
  });

  it("states what is lost and waits for the host name", () => {
    const html = render({
      setting: { on: false },
      system: { available: true, host: "box-1", reason: "Linux 4.18.0 has no Landlock" },
    });
    expect(html).toContain("read the files and reach the network of the OpenNeko");
    expect(html).toContain("<strong>box-1</strong>");
    expect(html).toMatch(/<button[^>]*disabled[^>]*>Run agent turns without confinement/);
  });

  it("shows who turned it on, in red, with a way to turn it off", () => {
    const html = render({
      setting: { on: true, by: "admin@example.com", at: "2026-11-21T10:00:00Z", reason: "no Landlock" },
      system: { available: true, host: "box-1" },
    });
    expect(html).toContain("text-danger");
    expect(html).toContain("admin@example.com turned this on");
    expect(html).toContain("Turn off");
  });
});
