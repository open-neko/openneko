import { readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { createScrubber, scrubAgentEvent } from "../src/work/secret-scrubber";

// The Go event store (apps/openneko/internal/webfront/turnscrub.go) applies
// this rule. Run with UPDATE_PROMPT_FIXTURES=1 to rewrite the fixture.
const FIXTURE = join(__dirname, "../../../apps/openneko/internal/webfront/testdata/runeventscrub.json");

const KEY = "sk-live-0123456789abcdef";
const TOKEN = "xoxb-planted-slack-token";
const VALUES = [KEY, TOKEN, "short", "sk-live-0123"];

const EVENTS: Record<string, unknown>[] = [
  { type: "message", role: "assistant", content: `The key is ${KEY}.` },
  { type: "interim", content: `${TOKEN} and sk-live-0123 alone` },
  { type: "tool_start", id: "t1", name: "terminal", input: { command: `curl -H "Authorization: ${KEY}"`, n: 3 } },
  { type: "tool_delta", id: "t1", delta: [`chunk ${TOKEN}`, { nested: KEY }] },
  { type: "tool_end", id: "t1", result: { rows: [{ secret: KEY, count: 2, ok: true, none: null }] }, error: `failed with ${TOKEN}` },
  { type: "tool_end", id: "t2", result: `plain ${KEY}` },
  { type: "surface", messages: [{ text: KEY }] },
  { type: "artifact", artifact: { title: "a", body: TOKEN } },
  { type: "status", message: `using ${KEY}`, content: TOKEN, code: KEY },
  { type: "progress", message: 5 },
  { type: "error", message: `boom ${KEY}` },
  { type: "done", result: { status: "completed", text: TOKEN } },
  { type: "done", status: "failed" },
  {
    type: "needs_input",
    question: `Use ${KEY}?`,
    options: [KEY, { label: KEY }],
    questions: [{ q: TOKEN }],
    reason: TOKEN,
    other: KEY,
  },
  { type: "needs_input", question: "q", questions: "not a list" },
  { type: "output_emit", output_id: KEY, kind: "report" },
  { type: "action_request_emit", action_request_id: "a1", kind: KEY, intent: `pay ${KEY}`, summary: TOKEN },
  { type: "action_request_result", action_request_id: "a1", outcome: { result: { body: KEY }, error: TOKEN } },
  { type: "usage", source: "outer", provider: "google-gemini", model: KEY, usage: { totalTokens: 7 } },
  { type: "telemetry", summary: { note: `${KEY}${KEY}` } },
  { type: "message", role: "user", content: "1234 nothing to hide" },
];

describe("Go run event scrub fixtures", () => {
  it("matches the stored fixture", () => {
    const scrub = createScrubber(VALUES);
    const cases = EVENTS.map((event) => ({ event, scrubbed: JSON.stringify(scrubAgentEvent(scrub, event)) }));
    const fixture = `${JSON.stringify({ values: VALUES, cases }, null, 2)}\n`;
    if (process.env.UPDATE_PROMPT_FIXTURES === "1") writeFileSync(FIXTURE, fixture);
    expect(fixture).toBe(readFileSync(FIXTURE, "utf8"));
  });
});
