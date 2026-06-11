// Runtime-agnostic plugin-runtime contract (SEC9): the network-mode
// translation and the exec-command builder every runtime shares.

import { describe, expect, it } from "vitest";
import {
  buildExecCommand,
  networkModeFor,
} from "../../src/plugins/plugin-runtime.js";

describe("networkModeFor", () => {
  it("returns 'none' for empty host list", () => {
    expect(networkModeFor([])).toBe("none");
  });

  it("returns 'public' for any declared host", () => {
    expect(networkModeFor(["api.example.com"])).toBe("public");
    expect(networkModeFor(["a.b.c", "*.example.org"])).toBe("public");
  });
});

describe("buildExecCommand", () => {
  it("with empty env, returns plain node exec (fast path)", () => {
    expect(buildExecCommand("register", "{}", {})).toEqual({
      cmd: "node",
      args: ["/workspace/run.js", "register", "{}"],
    });
  });

  it("with env, wraps in sh -c with exports + exec node", () => {
    const out = buildExecCommand("execute_action", '{"x":1}', {
      SLACK_BOT_TOKEN: "xoxb-abc",
      RUN_ID: "r-1",
    });
    expect(out.cmd).toBe("sh");
    expect(out.args[0]).toBe("-c");
    const inner = out.args[1] ?? "";
    expect(inner).toContain("export SLACK_BOT_TOKEN='xoxb-abc'");
    expect(inner).toContain("export RUN_ID='r-1'");
    expect(inner).toContain("exec node /workspace/run.js 'execute_action' '{\"x\":1}'");
  });

  it("POSIX-escapes single quotes inside env values", () => {
    const out = buildExecCommand("m", "{}", { FOO: "it's ok" });
    expect(out.args[1]).toContain(`export FOO='it'\\''s ok'`);
  });

  it("rejects bad env key names that could be shell-substring attacks", () => {
    expect(() =>
      buildExecCommand("m", "{}", { "FOO; rm -rf /": "x" }),
    ).toThrow(/UPPER_SNAKE_CASE/);
    expect(() =>
      buildExecCommand("m", "{}", { lowercase: "x" }),
    ).toThrow(/UPPER_SNAKE_CASE/);
  });
});
