import { describe, expect, it } from "vitest";
import { buildBridgeServer } from "../../src/agent-sandbox/mcp-bridge.js";
import { BrokerControlPlane } from "../../src/agent-sandbox/broker-client.js";

const SERVERS = [
  "neko_skills",
  "neko_memory",
  "neko_workflow_builder",
  "neko_rule_builder",
  "neko_plugin_manager",
  "neko_user_manager",
  "neko_channel_manager",
  "neko_data_source_manager",
  "neko_source_config_manager",
  "neko_audit",
  "neko_plugin_actions",
];

function ctx() {
  return {
    orgId: "org-1",
    threadId: "11111111-1111-4111-8111-111111111111",
    runId: "22222222-2222-4222-8222-222222222222",
    skillsRoot: "/tmp/skills",
    pluginActions: [
      {
        kind: "send_slack_message",
        pluginId: "@open-neko/plugin-slack",
        description: "send",
        scope: "external" as const,
      },
    ],
    controlPlane: new BrokerControlPlane("http://127.0.0.1:9", "tok"),
  };
}

describe("mcp-bridge buildBridgeServer", () => {
  it("constructs a connectable server for every name hermes mounts", () => {
    for (const name of SERVERS) {
      const server = buildBridgeServer(name, ctx());
      expect(server.instance, name).toBeTruthy();
      expect(typeof server.instance.connect, name).toBe("function");
    }
  });

  it("throws on unknown server names", () => {
    expect(() => buildBridgeServer("neko_nope", ctx())).toThrow(/unknown server/);
  });
});
