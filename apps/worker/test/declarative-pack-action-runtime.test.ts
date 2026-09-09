import { beforeEach, describe, expect, it, vi } from "vitest";

const query = vi.fn();
vi.mock("@neko/db", async (importOriginal) => ({
  ...await importOriginal<typeof import("@neko/db")>(),
  pool: () => ({ query }),
}));
vi.mock("@neko/llm/graphjin", async (importOriginal) => ({
  ...await importOriginal<typeof import("@neko/llm/graphjin")>(),
  graphjinQuery: vi.fn(),
  mintGraphjinToken: vi.fn(() => "executor-token"),
  resolvePackSource: vi.fn(async (_orgId, source) => source),
}));

import { graphjinQuery } from "@neko/llm/graphjin";
import { declarativePackActionAdapter } from "../src/packs/declarative-action-runtime.js";

const request = {
  id: "request-1",
  orgId: "org-1",
  kind: "google_workspace.sheets",
  status: "approved",
  approvedByUserId: "admin-1",
  payload: {
    operation: "update_values",
    path: { spreadsheetId: "sheet-1", range: "Prices!A2:B2" },
    query: { valueInputOption: "RAW" },
    body: { values: [["SKU-1", 19.99]] },
  },
} as Parameters<typeof declarativePackActionAdapter>[0]["request"];

beforeEach(() => {
  vi.clearAllMocks();
  query.mockResolvedValue({ rows: [{
    definition: {
      adapter: {
        kind: "graphjin_api_operation",
        source: "google_workspace_sheets",
        spec: "sheets",
        operations: {
          update_values: {
            operationId: "updateSpreadsheetValues",
            mutationRoot: "gws_sheets_update_values",
          },
        },
      },
    },
    enabled: true,
    readiness: "ready",
    readinessReason: null,
    installStatus: "installed",
    source: { id: "source-1", graphqlUrl: "https://graphjin.example", authMode: "jwt" },
  }] });
});

describe("declarative pack action runtime", () => {
  it("executes only the declared mutation with reviewed path, query, and body", async () => {
    vi.mocked(graphjinQuery).mockResolvedValue({
      data: { gws_sheets_update_values: { ok: true, status_code: 200, operation_id: "updateSpreadsheetValues" } },
    });
    await expect(declarativePackActionAdapter({ request })).resolves.toMatchObject({
      commandOrOperation: "updateSpreadsheetValues",
      result: { operation: "update_values", statusCode: 200 },
    });
    expect(vi.mocked(graphjinQuery).mock.calls[0]![0]).toMatchObject({
      role: "pack_api_executor",
      variables: { call: { path: request.payload.path, query: request.payload.query, body: request.payload.body } },
    });
  });

  it("rejects an operation that the installed pack did not declare", async () => {
    await expect(declarativePackActionAdapter({
      request: { ...request, payload: { ...request.payload, operation: "delete_spreadsheet" } },
    })).rejects.toThrow("unsupported operation");
    expect(graphjinQuery).not.toHaveBeenCalled();
  });
});
