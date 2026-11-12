import { createHash } from "node:crypto";
import { createServer, type Server } from "node:http";
import { existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import type { AddressInfo } from "node:net";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { clearKnowledgeSnapshot, KNOWLEDGE_FILES } from "../src/knowledge-cache";
import { prefetchAgenticKnowledgePack, prefetchKnowledgePack } from "../src/knowledge-pack";

// The Go prefetch (apps/openneko/internal/webfront/turnknowledge.go) must
// write the same files. Run with UPDATE_PROMPT_FIXTURES=1 to rewrite the fixture.
const TESTDATA = join(__dirname, "../../../apps/openneko/internal/webfront/testdata");
const FIXTURE = join(TESTDATA, "knowledgepack.json");

type Reply = { status: number; body: string };
type Group = { base: Record<string, Reply>; cases: Record<string, Record<string, Reply>> };
const catalog = JSON.parse(readFileSync(join(TESTDATA, "knowledgecatalog.json"), "utf8")) as { agentic: Group; legacy: Group };

let server: Server;
let baseUrl = "";
let replies: Record<string, Reply> = {};

beforeAll(async () => {
  server = createServer((req, res) => {
    let body = "";
    req.on("data", (chunk) => (body += chunk));
    req.on("end", () => {
      const key = req.method === "POST" ? (JSON.parse(body) as { query: string }).query : (req.url ?? "").replace(/^\/discovery\//, "");
      const reply = replies[key] ?? { status: 500, body: "{}" };
      res.writeHead(reply.status, { "content-type": "application/json" });
      res.end(reply.body);
    });
  });
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  baseUrl = `http://127.0.0.1:${(server.address() as AddressInfo).port}`;
});

afterAll(() => new Promise<void>((resolve) => server.close(() => resolve())));

const sha256 = (text: string) => createHash("sha256").update(text, "utf8").digest("hex");

function filesIn(dir: string) {
  return Object.fromEntries(KNOWLEDGE_FILES.map((file) => {
    const path = join(dir, file);
    if (!existsSync(path)) return [file, null];
    const text = readFileSync(path, "utf8");
    return [file, { bytes: Buffer.byteLength(text, "utf8"), sha256: sha256(text) }];
  }));
}

describe("knowledge pack fixtures for the Go prefetch", () => {
  it("matches the fixture the Go test reads", { timeout: 30_000 }, async () => {
    const built: unknown[] = [];
    for (const mode of ["agentic", "legacy"] as const) {
      for (const [name, overrides] of Object.entries(catalog[mode].cases)) {
        replies = { ...catalog[mode].base, ...overrides };
        const dir = mkdtempSync(join(tmpdir(), "knowledge-fixture-"));
        try {
          const result = mode === "agentic"
            ? await prefetchAgenticKnowledgePack({ graphqlUrl: `${baseUrl}/graphql`, token: "t", destDir: dir })
            : await prefetchKnowledgePack({ discoveryUrl: `${baseUrl}/discovery/`, destDir: dir });
          built.push({ mode, name, result, files: filesIn(dir) });
        } finally {
          rmSync(dir, { recursive: true, force: true });
        }
      }
    }
    const dir = mkdtempSync(join(tmpdir(), "knowledge-fixture-"));
    try {
      await clearKnowledgeSnapshot(dir, "none", "legacy");
      const text = readFileSync(join(dir, ".knowledge-snapshot.json"), "utf8");
      built.push({ mode: "clear", name: "none", snapshot: { bytes: Buffer.byteLength(text, "utf8"), sha256: sha256(text) } });
    } finally {
      rmSync(dir, { recursive: true, force: true });
    }
    const json = `${JSON.stringify(built, null, 2)}\n`;
    if (process.env.UPDATE_PROMPT_FIXTURES === "1") writeFileSync(FIXTURE, json);
    expect(existsSync(FIXTURE)).toBe(true);
    expect(readFileSync(FIXTURE, "utf8")).toBe(json);
  });
});
