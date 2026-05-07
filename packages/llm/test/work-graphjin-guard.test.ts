import { describe, expect, it } from "vitest";
import { isGraphjinCommandSafe } from "../src/work/graphjin-guard";

describe("isGraphjinCommandSafe", () => {
  it("allows read-style graphjin queries", () => {
    expect(
      isGraphjinCommandSafe([
        "cli",
        "execute_graphql",
        "--args",
        '{"query":"query Revenue { revenue { total } }"}',
      ]),
    ).toBe(true);
  });

  it("blocks mutations", () => {
    expect(
      isGraphjinCommandSafe([
        "cli",
        "execute_graphql",
        "--args",
        '{"query":"mutation Dangerous { delete_user(id: 1) }"}',
      ]),
    ).toBe(false);
  });

  it("blocks config-changing commands", () => {
    expect(isGraphjinCommandSafe(["config", "set", "admin_secret", "x"])).toBe(false);
  });
});
