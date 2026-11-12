import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("@neko/secret-crypt", () => ({ deriveSigningSecret: () => Buffer.alloc(32, 1) }));

import { isLibraryStorePath, libraryStoreFilePath, storeLibraryFile } from "../src/library/store";

describe("Library store", () => {
  afterEach(() => {
    vi.unstubAllEnvs();
    vi.unstubAllGlobals();
  });

  it("puts an upload in the user's folder under its content hash", async () => {
    vi.stubEnv("OPENNEKO_SERVE_URL", "http://serve.test/");
    const calls: Array<{ url: string; method: string }> = [];
    vi.stubGlobal("fetch", vi.fn(async (url: string, init: RequestInit) => {
      calls.push({ url, method: String(init.method) });
      return new Response(JSON.stringify({ path: "x", size: 1, modified_at: "" }), { status: 200 });
    }));
    const stored = await storeLibraryFile({ orgId: "o", userId: "u", filename: "a.md", data: Buffer.from("hello") });
    const hash = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824";
    expect(stored).toEqual({ relativePath: `files/library/${hash}/a.md`, contentHash: hash, size: 5 });
    expect(calls).toEqual([{ url: `http://serve.test/v1/files/content?path=${encodeURIComponent(`library/${hash}/a.md`)}`, method: "PUT" }]);
  });

  it("tells moved paths from older workspace paths", () => {
    expect(isLibraryStorePath("files/library/h/a.md")).toBe(true);
    expect(isLibraryStorePath("library/uploads/u/h/a.md")).toBe(false);
    expect(isLibraryStorePath("uploads/t/a.md")).toBe(false);
    expect(libraryStoreFilePath("files/library/h/a.md")).toBe("library/h/a.md");
    expect(() => libraryStoreFilePath("uploads/t/a.md")).toThrow();
  });

  it("fails when openneko serve is off", async () => {
    vi.stubEnv("OPENNEKO_SERVE_URL", "");
    await expect(storeLibraryFile({ orgId: "o", userId: "u", filename: "a.md", data: Buffer.from("x") })).rejects.toThrow(/off/);
  });
});
