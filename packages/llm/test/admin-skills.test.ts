import { describe, expect, it } from "vitest";
import { runSkillScope } from "../src/work/admin-skills";

describe("runSkillScope", () => {
  const installed = ["graphjin-config", "internal-comms", "xlsx"];

  it("keeps an admin run's entitlements as they are", () => {
    expect(runSkillScope(null, installed, true)).toBeUndefined();
    expect(runSkillScope(["graphjin-config"], installed, true)).toEqual(["graphjin-config"]);
  });

  it("takes the admin-only skills out of every other run", () => {
    expect(runSkillScope(null, installed, false)).toEqual(["internal-comms", "xlsx"]);
    expect(runSkillScope(["graphjin-config", "xlsx"], installed, false)).toEqual(["xlsx"]);
    expect(runSkillScope(undefined, [], false)).toEqual([]);
  });
});
