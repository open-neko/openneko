import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  getOrgId: vi.fn(async () => "org-a"),
  list: vi.fn(),
  save: vi.fn(),
  remove: vi.fn(),
}));

vi.mock("@/lib/db", () => ({ getOrgId: mocks.getOrgId }));
vi.mock("@/lib/records", () => ({
  listRecordSavedViews: mocks.list,
  saveRecordSavedView: mocks.save,
  deleteRecordSavedView: mocks.remove,
}));
vi.mock("@/lib/records-api", async () => {
  const { NextResponse } = await import("next/server");
  return {
    recordsApiError: (error: unknown) =>
      NextResponse.json(
        { error: error instanceof Error ? error.message : "failed" },
        { status: 400 },
      ),
  };
});

import {
  GET,
  POST,
} from "@/app/api/a/[app]/[object]/views/route";
import {
  DELETE,
  POST as POST_DELETE,
} from "@/app/api/a/[app]/[object]/views/[id]/route";

const context = {
  params: Promise.resolve({ app: "operations", object: "work_order" }),
};
const deleteContext = {
  params: Promise.resolve({
    app: "operations",
    object: "work_order",
    id: "00000000-0000-4000-8000-000000000321",
  }),
};
const definition = {
  version: 1,
  filter: { field: "status", operator: "is_open" },
  sort: null,
  search: null,
  myRecords: false,
  columns: ["name", "status"],
};

describe("records saved-view routes", () => {
  beforeEach(() => vi.clearAllMocks());

  it("lists and saves typed definitions within the route scope", async () => {
    mocks.list.mockResolvedValue([{ id: "view-1", label: "Open" }]);
    const listed = await GET(new Request("http://localhost"), context);
    expect(await listed.json()).toEqual({ views: [{ id: "view-1", label: "Open" }] });
    expect(mocks.list).toHaveBeenCalledWith({
      orgId: "org-a",
      appId: "operations",
      objectApiName: "work_order",
    });

    mocks.save.mockResolvedValue({ id: "view-2", label: "Open", definition });
    const saved = await POST(
      new Request("http://localhost/api/a/operations/work_order/views", {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ label: "Open", shared: true, definition }),
      }),
      context,
    );
    expect(saved.status).toBe(201);
    expect(mocks.save).toHaveBeenCalledWith({
      orgId: "org-a",
      appId: "operations",
      objectApiName: "work_order",
      label: "Open",
      shared: true,
      definition,
    });
  });

  it("supports API deletion and browser-form deletion redirects", async () => {
    mocks.remove.mockResolvedValue(true);
    const deleted = await DELETE(
      new Request("http://localhost/api/a/operations/work_order/views/view", {
        method: "DELETE",
      }),
      deleteContext,
    );
    expect(deleted.status).toBe(204);
    expect(mocks.remove).toHaveBeenCalledWith({
      orgId: "org-a",
      appId: "operations",
      objectApiName: "work_order",
      id: "00000000-0000-4000-8000-000000000321",
    });

    const redirected = await POST_DELETE(
      new Request("http://localhost/api/a/operations/work_order/views/view", {
        method: "POST",
      }),
      deleteContext,
    );
    expect(redirected.status).toBe(303);
    expect(redirected.headers.get("location")).toBe(
      "/a/operations/work_order",
    );
  });

  it("rejects malformed form definitions before storage", async () => {
    const form = new FormData();
    form.set("label", "Broken");
    form.set("definition", "{not json");
    const response = await POST(
      new Request("http://localhost/api/a/operations/work_order/views", {
        method: "POST",
        body: form,
      }),
      context,
    );
    expect(response.status).toBe(400);
    expect(mocks.save).not.toHaveBeenCalled();
  });
});
