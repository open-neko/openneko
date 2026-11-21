import { describe, expect, it } from "vitest";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import UnconfinedTurnsField, { type UnconfinedTurnsState } from "@/app/admin/settings/agent/UnconfinedTurnsField";

const commands = {
  allow: 'openneko confinement allow-unconfined --reason "<why this host cannot confine>"',
  deny: "openneko confinement deny",
};

function render(initial: Omit<UnconfinedTurnsState, "commands">) {
  return renderToStaticMarkup(createElement(UnconfinedTurnsField, { initial: { ...initial, commands } }));
}

describe("UnconfinedTurnsField", () => {
  it("offers nothing on a system that can confine", () => {
    const html = render({
      setting: { on: false },
      applies: false,
      system: { available: false, blocked: "This system confines agent turns, so the override stays off." },
    });
    expect(html).toContain("This system confines agent turns");
    expect(html).not.toContain("allow-unconfined");
  });

  it("states what is lost and gives the host command, with no control that changes it", () => {
    const html = render({
      setting: { on: false },
      applies: false,
      system: { available: true, host: "box-1", reason: "Linux 4.18.0 has no Landlock" },
    });
    expect(html).toContain("read the files and reach the network of the OpenNeko");
    expect(html).toContain("openneko confinement allow-unconfined");
    expect(html).not.toMatch(/<(button|input|form)/);
  });

  it("says why an override from another host does not apply", () => {
    const html = render({
      setting: { on: true, host: "box-1" },
      applies: false,
      notApplied: "the override was given for host box-1, not for this host",
      system: { available: true, host: "box-2" },
    });
    expect(html).toContain("does not apply: the override was given for host box-1");
  });

  it("shows the host user in red, with the command that refuses it again", () => {
    const html = render({
      setting: { on: true, hostUser: "neko-ops", host: "box-1", kernel: "Linux 4.18.0", at: "2026-11-21T10:00:00Z", reason: "no Landlock" },
      applies: true,
      system: { available: true, host: "box-1" },
    });
    expect(html).toContain("text-danger");
    expect(html).toContain("Host user neko-ops allowed it on box-1");
    expect(html).toContain("openneko confinement deny");
    expect(html).not.toMatch(/<(button|input|form)/);
  });
});
