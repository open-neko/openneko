import { describe, expect, it } from "vitest";
import { definePlugin } from "../src/define-plugin";
import { ListDirectoryRpcResult, RegisterResult } from "../src/rpc";
import { dispatchPluginRpc } from "../src/runner";
import { PluginCapabilitiesDeclaration } from "../src/manifest";

const directoryPlugin = definePlugin({
  name: "@open-neko/plugin-directory-example",
  version: "0.1.0",
  capabilities: {
    directory: {
      providerLabel: "Example IdP",
      write: { createUser: true },
      list: ({ cursor }) =>
        cursor
          ? { tenantId: "t1", users: [{ externalId: "u2", email: "b@example.test", active: false }] }
          : {
              tenantId: "t1",
              users: [{ externalId: "u1", email: "a@example.test", sub: "sub-a" }],
              groups: [{ externalId: "g1", name: "Finance" }],
              memberships: [{ userExternalId: "u1", groupExternalId: "g1" }],
              nextCursor: "page-2",
            },
      apply: ({ change }) => ({
        user: change.op === "create_user" ? { externalId: "u3", email: change.email, active: true } : null,
      }),
    },
  },
});

describe("directory capability", () => {
  it("registers the declaration with read defaults and declared writes", async () => {
    const response = await dispatchPluginRpc(directoryPlugin, { method: "register", paramsJson: "{}" });
    if (!response.ok) throw new Error(response.error.message);
    expect(RegisterResult.parse(response.result).capabilities.directory).toEqual({
      providerLabel: "Example IdP",
      read: { users: true, groups: true, memberships: true },
      write: { createUser: true, deactivateUser: false },
    });
  });

  it("pages list_directory results and applies defaults", async () => {
    const first = await dispatchPluginRpc(directoryPlugin, { method: "list_directory", paramsJson: JSON.stringify({ params: {} }) });
    if (!first.ok) throw new Error(first.error.message);
    const page = ListDirectoryRpcResult.parse(first.result).result;
    expect(page.nextCursor).toBe("page-2");
    expect(page.users[0]).toEqual({ externalId: "u1", email: "a@example.test", sub: "sub-a", active: true });

    const second = await dispatchPluginRpc(directoryPlugin, {
      method: "list_directory",
      paramsJson: JSON.stringify({ params: { cursor: "page-2" } }),
    });
    if (!second.ok) throw new Error(second.error.message);
    expect(ListDirectoryRpcResult.parse(second.result).result).toMatchObject({ groups: [], memberships: [] });
  });

  it("accepts declared writes and refuses undeclared ones", async () => {
    const created = await dispatchPluginRpc(directoryPlugin, {
      method: "apply_directory_change",
      paramsJson: JSON.stringify({ params: { change: { op: "create_user", email: "c@example.test" } } }),
    });
    expect(created).toMatchObject({ ok: true, result: { result: { user: { externalId: "u3" } } } });
    const refused = await dispatchPluginRpc(directoryPlugin, {
      method: "apply_directory_change",
      paramsJson: JSON.stringify({ params: { change: { op: "deactivate_user", externalId: "u1" } } }),
    });
    expect(refused).toMatchObject({ ok: false, error: { message: 'plugin does not accept directory change "deactivate_user"' } });
  });

  it("rejects a malformed page from the plugin", async () => {
    const bad = definePlugin({
      name: "@open-neko/plugin-bad-directory",
      version: "0.1.0",
      capabilities: { directory: { providerLabel: "Bad", list: () => ({ users: [] }) as never } },
    });
    const response = await dispatchPluginRpc(bad, { method: "list_directory", paramsJson: JSON.stringify({ params: {} }) });
    expect(response.ok).toBe(false);
  });

  it("validates definePlugin and the manifest declaration", () => {
    expect(() =>
      definePlugin({
        name: "@open-neko/plugin-x",
        version: "0.1.0",
        capabilities: { directory: { providerLabel: "X", write: { deactivateUser: true }, list: () => ({ tenantId: "t" }) as never } },
      }),
    ).toThrow("capabilities.directory.apply must be a function");
    expect(PluginCapabilitiesDeclaration.safeParse({ directory: { providerLabel: "X" } }).success).toBe(true);
  });
});
