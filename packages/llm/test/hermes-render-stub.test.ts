import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { InMemoryTransport } from "@modelcontextprotocol/sdk/inMemory.js";
import { describe, expect, it, vi } from "vitest";
import { buildRenderCardsServer, buildSourceConfigManagerServer } from "../src/work/tools";
import type { NekoMcpServer } from "../src/mcp-server";

async function withServer(
  server: NekoMcpServer,
  run: (client: Client) => Promise<void>,
) {
  const [clientTransport, serverTransport] = InMemoryTransport.createLinkedPair();
  await (server as Extract<NekoMcpServer, { type: "sdk" }>).instance.connect(serverTransport);
  const client = new Client({ name: "render-server-test", version: "1.0.0" });
  await client.connect(clientTransport);
  try {
    await run(client);
  } finally {
    await client.close();
  }
}

const text = (result: Awaited<ReturnType<Client["callTool"]>>) =>
  (result.content as Array<{ text: string }>)[0]?.text;

describe("brokered neko_ui render MCP server", () => {
  it("emits one Answer surface for a valid answer", async () => {
    const emit = vi.fn(async () => {});
    await withServer(buildRenderCardsServer(emit), async (client) => {
      const listed = await client.listTools();
      expect(listed.tools.map((tool) => tool.name)).toEqual(["render_cards"]);
      const result = await client.callTool({
        name: "render_cards",
        arguments: { title: "Orders", keyFigures: ["Orders: 42"] },
      });
      expect(result.isError).toBeFalsy();
      expect(text(result)).toBe("Cards shown.");
      expect(emit).toHaveBeenCalledTimes(1);
      expect(emit.mock.calls[0]?.[0]).toMatchObject({
        type: "surface",
        messages: [{
          version: "v1.0",
          createSurface: {
            surfaceId: expect.stringMatching(/^answer-/),
            components: [
              { id: "root", component: "Answer", title: "Orders", children: ["keyFigures"] },
              { id: "keyFigures", component: "KeyFigures", items: [{ label: "Orders", value: "42" }] },
            ],
          },
        }],
      });
    });
  });

  it("returns one corrective line per problem and emits nothing", async () => {
    const emit = vi.fn(async () => {});
    await withServer(buildRenderCardsServer(emit), async (client) => {
      const result = await client.callTool({
        name: "render_cards",
        arguments: { title: "Orders", table: "A | B\n1" },
      });
      expect(result.isError).toBe(true);
      expect(text(result)).toBe(
        "Cards not shown. Fix and call again:\n- table row 1 needs 2 cells, one per column.",
      );
      expect(emit).not.toHaveBeenCalled();
    });
  });

  it("rejects the previous message envelope before any surface is emitted", async () => {
    const emit = vi.fn(async () => {});
    await withServer(buildRenderCardsServer(emit), async (client) => {
      const result = await client.callTool({
        name: "render_cards",
        arguments: { messages: [{ version: "v1.0", createSurface: "{}" }] },
      });
      expect(result.isError).toBe(true);
      expect(emit).not.toHaveBeenCalled();
    });
  });
});

describe("present_source_form", () => {
  it("emits the canonical source form with the agent's prefill", async () => {
    const emit = vi.fn(async () => {});
    const server = buildSourceConfigManagerServer({
      orgId: "org",
      runId: "run",
      emit,
      controlPlane: {} as never,
    });
    await withServer(server, async (client) => {
      const result = await client.callTool({
        name: "present_source_form",
        arguments: { name: "warehouse", kind: "database", host: "db.internal", secretRef: "warehouse-password" },
      });
      expect(text(result)).toBe("Form shown.");
    });
    const surface = (emit.mock.calls[0]?.[0] as { messages: Array<{ createSurface: Record<string, unknown> }> })
      .messages[0]!.createSurface;
    expect(surface.surfaceId).toMatch(/^source-form-/);
    expect(surface.dataModel).toMatchObject({
      form: {
        name: "warehouse",
        kind: ["database"],
        type: ["postgres"],
        host: "db.internal",
        port: 5432,
        secretRef: "warehouse-password",
        blockedTables: "",
        adminTables: "",
        backend: ["local"],
      },
    });
    const components = surface.components as Array<{ id: string; component: string }>;
    expect(components.find((component) => component.id === "root")).toMatchObject({ component: "Answer" });
    expect(components.filter((component) => component.component === "Button").map((component) => component.id))
      .toEqual(["submitDatabase", "submitApi", "submitLocalFile", "submitCloudFile"]);
    expect(components.find((component) => component.id === "password")).toMatchObject({
      component: "SourceSecretInput",
      value: { path: "/form/secretRef" },
    });
    expect(components.find((component) => component.id === "type")).toMatchObject({
      component: "ChoicePicker",
      options: [
        { label: "PostgreSQL", value: "postgres" },
        { label: "MySQL", value: "mysql" },
        { label: "MariaDB", value: "mariadb" },
      ],
    });
    expect(JSON.stringify(surface.dataModel)).not.toMatch(/password"?\s*:/i);
  });

  it("prefills a MySQL type and refuses an unknown one", async () => {
    const emit = vi.fn(async () => {});
    const server = buildSourceConfigManagerServer({
      orgId: "org",
      runId: "run",
      emit,
      controlPlane: {} as never,
    });
    await withServer(server, async (client) => {
      await client.callTool({ name: "present_source_form", arguments: { type: "mysql" } });
      const refused = await client.callTool({ name: "present_source_form", arguments: { type: "oracle" } });
      expect(refused.isError).toBe(true);
    });
    expect(emit).toHaveBeenCalledTimes(1);
    const surface = (emit.mock.calls[0]?.[0] as { messages: Array<{ createSurface: { dataModel: { form: Record<string, unknown> } } }> })
      .messages[0]!.createSurface;
    expect(surface.dataModel.form.type).toEqual(["mysql"]);
  });
});

describe("request_source_config_change", () => {
  it("passes set_source_tables to the preview and refuses a bad table name", async () => {
    const previewSourceConfigChange = vi.fn(async () => ({ denied: true }));
    const server = buildSourceConfigManagerServer({
      orgId: "org",
      runId: "run",
      emit: vi.fn(async () => {}),
      controlPlane: { previewSourceConfigChange } as never,
    });
    await withServer(server, async (client) => {
      const result = await client.callTool({
        name: "request_source_config_change",
        arguments: {
          action: "set_source_tables",
          source: "shop",
          blockedTables: ["oauth_token"],
          adminTables: ["sales_invoice"],
          intent: "Hide the token table and keep invoices for admins.",
        },
      });
      expect(JSON.parse(text(result))).toMatchObject({ ok: false, denied: true });
      const refused = await client.callTool({
        name: "request_source_config_change",
        arguments: {
          action: "set_source_tables",
          source: "shop",
          blockedTables: ["orders; drop table x"],
          intent: "Hide orders.",
        },
      });
      expect(refused.isError).toBe(true);
    });
    expect(previewSourceConfigChange).toHaveBeenCalledTimes(1);
    expect(previewSourceConfigChange).toHaveBeenCalledWith({
      orgId: "org",
      runId: "run",
      dataSourceId: undefined,
      payload: {
        action: "set_source_tables",
        source: "shop",
        blockedTables: ["oauth_token"],
        adminTables: ["sales_invoice"],
      },
    });
  });

  it("shows the form again with each problem beside its answer", async () => {
    const emit = vi.fn(async () => {});
    const previewSourceConfigChange = vi.fn(async () => ({
      error: "The login name is empty. Save the password for this login first.",
      problems: [
        { field: "user", message: "The login name is empty." },
        { field: "secretRef", message: "Save the password for this login first." },
        { field: null, message: "GraphJin did not accept this change.", detail: "something else" },
      ],
    }));
    const server = buildSourceConfigManagerServer({
      orgId: "org",
      runId: "run",
      emit,
      controlPlane: { previewSourceConfigChange } as never,
    });
    await withServer(server, async (client) => {
      const result = await client.callTool({
        name: "request_source_config_change",
        arguments: {
          action: "register_source",
          name: "shop",
          kind: "database",
          type: "mysql",
          host: "db.internal",
          port: 3306,
          dbname: "magento",
          blockedTables: ["admin_user", "oauth_token"],
          intent: "Connect the store database.",
        },
      });
      const body = JSON.parse(text(result));
      expect(body).toMatchObject({
        ok: false,
        stage: "answer_check",
        problems: [{ field: "user" }, { field: "secretRef" }, { field: null }],
      });
      expect(body.next).toMatch(/form is shown again/);
    });
    expect(emit).toHaveBeenCalledTimes(1);
    const surface = (emit.mock.calls[0]?.[0] as { messages: Array<{ createSurface: { dataModel: Record<string, Record<string, unknown>>; components: Array<Record<string, unknown>> } }> })
      .messages[0]!.createSurface;
    expect(surface.dataModel.form).toMatchObject({
      name: "shop",
      kind: ["database"],
      type: ["mysql"],
      host: "db.internal",
      port: 3306,
      dbname: "magento",
      user: "",
      blockedTables: "admin_user, oauth_token",
    });
    expect(surface.dataModel.problems).toEqual({
      user: "The login name is empty.",
      secretRef: "Save the password for this login first.",
      general: "GraphJin did not accept this change.",
    });
    const byId = new Map(surface.components.map((component) => [component.id, component]));
    expect(byId.get("user")).toMatchObject({ error: { path: "/problems/user" } });
    expect(byId.get("password")).toMatchObject({ error: { path: "/problems/secretRef" } });
    expect(byId.get("problemsNote")).toMatchObject({ component: "Conditional", when: { path: "/problems/general" } });
  });

  it("shows no form for a change with no form", async () => {
    const emit = vi.fn(async () => {});
    const previewSourceConfigChange = vi.fn(async () => ({
      error: 'No source is named "shopp".',
      problems: [{ field: "source", message: 'No source is named "shopp". The sources are: shop.' }],
    }));
    const server = buildSourceConfigManagerServer({
      orgId: "org",
      runId: "run",
      emit,
      controlPlane: { previewSourceConfigChange } as never,
    });
    await withServer(server, async (client) => {
      const result = await client.callTool({
        name: "request_source_config_change",
        arguments: { action: "set_source_access", source: "shopp", read: "admin", intent: "Admins only." },
      });
      expect(JSON.parse(text(result))).toMatchObject({
        ok: false,
        stage: "answer_check",
        problems: [{ field: "source" }],
        next: expect.stringMatching(/which answer to fix/),
      });
    });
    expect(emit).not.toHaveBeenCalled();
  });
});
