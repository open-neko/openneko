import { describe, expect, it, vi } from "vitest";
import { moveLibraryDocumentsToStore } from "../src/jobs/library-recovery";

describe("moveLibraryDocumentsToStore", () => {
  it("moves workspace uploads into the user's Library folder and drops the original", async () => {
    const list = vi.fn(async () => [
      { orgId: "org-1", documentId: "doc-lib", userId: "user-1", filename: "policy.pdf", relativePath: "library/uploads/user-1/h/policy.pdf" },
      { orgId: "org-1", documentId: "doc-thread", userId: "user-1", filename: "notes.md", relativePath: "uploads/thread-1/notes.md" },
    ]);
    const read = vi.fn(async () => Buffer.from("bytes"));
    const save = vi.fn(async (input: { orgId: string; userId: string; filename: string }) => ({
      relativePath: `files/library/hash/${input.filename}`,
      contentHash: "hash",
      size: 5,
    }));
    const moved = vi.fn(async () => undefined);
    const failed = vi.fn(async () => undefined);
    const removeOriginal = vi.fn(async () => undefined);

    await expect(moveLibraryDocumentsToStore({ list, read, save, moved, failed, removeOriginal }))
      .resolves.toEqual({ moved: 2, missing: 0 });
    expect(moved).toHaveBeenCalledWith({ orgId: "org-1", id: "doc-lib", relativePath: "files/library/hash/policy.pdf" });
    expect(save).toHaveBeenCalledWith(expect.objectContaining({ orgId: "org-1", userId: "user-1", filename: "policy.pdf" }));
    // Library uploads leave the workspace; thread attachments stay with their thread.
    expect(removeOriginal).toHaveBeenCalledOnce();
    expect(removeOriginal).toHaveBeenCalledWith("org-1", "library/uploads/user-1/h/policy.pdf");
  });

  it("marks a document whose original file is gone as failed", async () => {
    const list = vi.fn(async () => [
      { orgId: "org-1", documentId: "doc-gone", userId: "user-1", filename: "a.pdf", relativePath: "library/uploads/user-1/h/a.pdf" },
    ]);
    const read = vi.fn(async () => { throw new Error("ENOENT"); });
    const save = vi.fn();
    const failed = vi.fn(async () => undefined);

    await expect(moveLibraryDocumentsToStore({ list, read, save, failed, moved: vi.fn(), removeOriginal: vi.fn() }))
      .resolves.toEqual({ moved: 0, missing: 1 });
    expect(save).not.toHaveBeenCalled();
    expect(failed).toHaveBeenCalledWith(expect.objectContaining({ id: "doc-gone", error: expect.stringContaining("missing") }));
  });

  it("marks a document without an owner as failed and stores nothing", async () => {
    const list = vi.fn(async () => [
      { orgId: "org-1", documentId: "doc-solo", userId: null, filename: "a.pdf", relativePath: "library/uploads/solo/h/a.pdf" },
    ]);
    const read = vi.fn();
    const save = vi.fn();
    const failed = vi.fn(async () => undefined);

    await expect(moveLibraryDocumentsToStore({ list, read, save, failed, moved: vi.fn(), removeOriginal: vi.fn() }))
      .resolves.toEqual({ moved: 0, missing: 1 });
    expect(read).not.toHaveBeenCalled();
    expect(save).not.toHaveBeenCalled();
    expect(failed).toHaveBeenCalledWith(expect.objectContaining({ id: "doc-solo", error: expect.stringContaining("owner") }));
  });
});
