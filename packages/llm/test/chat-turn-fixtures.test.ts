import { readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { extractMemoryFences } from "../src/agent-backends/memory-fence";
import {
  extractActionRequestFences,
  extractFollowupsFence,
  extractRuleSaveFence,
  extractValueFence,
  extractVitalsFence,
  extractWorkflowSaveFence,
} from "../src/workflows/fence-parsers";
import { clampAnalysisMinutes } from "../src/workflows/value";
import { extractNetworkPolicyDenial, needsInputText, stripDanglingToolCalls } from "../src/work/run-chat-turn";
import { structuredTurnSummary } from "../src/work/structured-turn-summary";

// The Go chat turn (apps/openneko/internal/webfront/turnchat.go) must read the
// answer fences and build the stored text as runChatTurn() does. Run with
// UPDATE_PROMPT_FIXTURES=1 to rewrite the fixture.
const FIXTURE = join(__dirname, "../../../apps/openneko/internal/webfront/testdata/chatturn.json");

const FENCES = [
  "Plain answer with no fence.",
  "Revenue rose.\n\n```neko_value\n{\"minutes_saved\": 12, \"basis\": \" two reports \"}\n```\n\n\n\nThanks.",
  "```neko_value\n{\"minutes_saved\": 5}\n```\n```NEKO_VALUE {\"minutes_saved\": 700}```\n```neko_value\n{\"minutes_saved\": 9}\n```",
  "```neko_value\nnot json\n```",
  "```neko_followups\n{\"followups\": [\" Which region? \", \"By month?\"]}\n```",
  "```neko_followups\n{\"followups\": [\"a\", \"b\", \"c\", \"d\"]}\n```",
  "```neko_vitals\n{\"vitals\": [{\"label\": \"Revenue\", \"value\": \"₹4.2 Cr\", \"sub\": \"\", \"basis\": \"observed\", \"asOf\": \"Oct 2026\", \"source\": \"orders\", \"extra\": 1}]}\n```",
  "```neko_vitals\n{\"vitals\": [{\"label\": \"A label that is far too long to fit\", \"value\": \"1\"}]}\n```",
  "Done.\n```neko_workflow_save\n{\"name\": \" Low stock \", \"steps\": [{\"id\": \"s1\", \"description\": \"Check stock\"}], \"triggers\": {\"cron\": \"0 9 * * *\"}}\n```\n```neko_workflow_save\n{\"name\": \"Second\", \"steps\": [{\"id\": \"s1\", \"description\": \"x\"}]}\n```",
  "```neko_workflow_save\n{\"name\": \"\", \"steps\": []}\n```\n```neko_workflow_save\n{oops}\n```",
  "```neko_workflow_save\n{\"name\": \"First\", \"steps\": [{\"id\": \"s1\", \"description\": \"x\"}]}\n```\n```neko_workflow_save\n{\"name\": \"\"}\n```\n```neko_rule_save\n{\"name\": \"R\", \"applies_to_kinds\": [], \"mode\": \"never\"}\n```\n```neko_rule_save\nnope\n```",
  "```neko_rule_save\n{\"name\": \"Slack auto\", \"applies_to_kinds\": [\"slack_post\"], \"mode\": \"auto_approve\", \"approver_role\": \"admin\"}\n```",
  "```neko_rule_save\n{\"name\": \"Bad\", \"applies_to_kinds\": [], \"mode\": \"sometimes\"}\n```",
  "```neko_action_request\n{\"scope\": \"external\", \"kind\": \"slack_post\", \"target\": \"C1\", \"payload\": {\"text\": \"hi\"}, \"summary\": \" Post the summary \", \"minutes_saved\": 8, \"status\": \"approved\"}\n```\n```neko_action_request\n{\"scope\": \"external\", \"kind\": \"email\", \"summary\": \"Mail\", \"risk_level\": \"high\"}\n```\n```neko_action_request\n{\"scope\": \"nowhere\", \"kind\": \"x\", \"summary\": \"y\"}\n```",
  "Saved.\n```neko_memory\n[{\"save\": {\"text\": \" Revenue is net of refunds \", \"scope\": \"thread\", \"pinned\": false}}, {\"save\": {\"text\": \"ab\"}}, {\"other\": 1}]\n```\nAnd ```neko_memory\n{\"save\": {\"text\": \"Fiscal year starts in April\", \"scope\": \"team\"}}\n``` then ```neko_memory\nbroken\n```",
  "Text before.\n```neko_action_request\n{\"scope\": \"external\", \"kind\": \"k\", \"summary\": \"s\"}\n```\n```neko_value\n{\"minutes_saved\": 3}\n```\n```neko_followups\n{\"followups\": [\"x\"]}\n```\n```neko_vitals\n{\"vitals\": []}\n```\n```neko_rule_save\n{}\n```\n```neko_memory\n[]\n```\nText after. call:default_api:read_file{limit:100,path:analyze.py}",
];

const DANGLING = [
  "Here is the answer.",
  "Partial work.\ncall:default_api:read_file{limit:100,path:analyze.py}",
  "call:x:y {a:1}   \nNext line  \t\nend call:ns:tool{unclosed",
  "",
];

const QUESTIONS = [
  { type: "needs_input", question: "Which store?", options: ["North", "South"] },
  { type: "needs_input", question: "Which store?", reason: "Two stores match." },
  {
    type: "needs_input",
    question: "ignored",
    questions: [
      { id: "a", question: "Which period?", options: [{ label: "Q3" }, { label: "Q4" }] },
      { id: "b", question: "Which currency?" },
    ],
  },
  { type: "needs_input", questions: [{ id: "a", question: "One question" }] },
];

const DENIALS = [
  { type: "tool_end", id: "1", name: "web_fetch", result: { text: "host api.Example.com:8443 is not on the web allowlist" } },
  { type: "tool_end", id: "2", name: "web_fetch", error: "host docs.example.org is not on the web allowlist" },
  { type: "tool_end", id: "3", name: "terminal", result: [{ out: ["POST hooks.example.com/v1/x?y=1 not permitted by policy"] }] },
  { type: "tool_end", id: "4", name: "terminal", result: "policy_denied for a reason with no request line" },
  { type: "tool_end", id: "5", name: "terminal", result: { a: { b: { c: { d: { e: { f: "GET deep.example not permitted by policy" } } } } } } },
  { type: "tool_start", id: "6", name: "web_fetch", input: { text: "host a.example is not on the web allowlist" } },
  { type: "tool_end", id: "7", name: "terminal", result: "delete Api.Example.COM:80 not permitted by policy" },
];

const SURFACE = [
  {
    createSurface: {
      surfaceId: "s1",
      dataModel: { rows: [{ region: "North", total: 12, note: null }, { region: "South", total: 7.5 }], series: [{ d: "Jan", v: 3 }, { d: "Feb", v: "x" }] },
      components: [
        { id: "t", component: "Text", text: "  Revenue by region  " },
        { id: "k", component: "KeyFigures", title: "Revenue by region", items: [{ label: "Total", value: 19.5, sub: "INR lakh", basis: " " }, { label: "Bad" }, "x"] },
        { id: "tb", component: "Table", columns: [{ key: "region" }, { key: "total" }, { label: "no key" }], rows: { path: "/rows" } },
        { id: "c", component: "Chart", valueLabel: " orders ", data: { path: "/series" } },
        { id: "c2", component: "Chart", data: [{ d: "Mar", v: 4 }] },
      ],
    },
  },
  { updateComponents: { components: [{ id: "x", component: "Table", rows: [{ a: "1", b: true, "0": "zero" }] }] } },
];

const SUMMARIES = [
  { vitals: null, surfaces: [], notes: [] },
  { vitals: { vitals: [{ label: "Revenue", value: "4.2", sub: "Cr", basis: "observed", asOf: "Oct", source: "orders" }, { label: "Orders", value: "120" }] }, surfaces: [], notes: [" note ", "note", ""] },
  { vitals: null, surfaces: SURFACE, notes: ["Streamed text"] },
  { vitals: null, surfaces: [], notes: ["x".repeat(6_100)] },
];

const MINUTES = [12, 12.5, -4, 200, null, Number.NaN, 179.4];

function fences(raw: string) {
  const reasons = (errors: { reason: string }[]) => errors.map((e) => e.reason);
  const workflow = extractWorkflowSaveFence(raw);
  const actions = extractActionRequestFences(raw);
  const rule = extractRuleSaveFence(raw);
  const value = extractValueFence(raw);
  const followups = extractFollowupsFence(raw);
  const vitals = extractVitalsFence(raw);
  const memory = extractMemoryFences(raw);
  let display = extractActionRequestFences(raw.trim()).text;
  display = extractWorkflowSaveFence(display).text;
  display = extractRuleSaveFence(display).text;
  display = extractValueFence(display).text;
  display = extractFollowupsFence(display).text;
  display = extractVitalsFence(display).text;
  display = extractMemoryFences(display).text;
  return {
    raw,
    workflow: { text: workflow.text, payload: workflow.payload, errors: reasons(workflow.errors) },
    actions: { text: actions.text, payloads: actions.payloads, errors: reasons(actions.errors) },
    rule: { text: rule.text, payload: rule.payload, errors: reasons(rule.errors) },
    value: { text: value.text, payload: value.payload, errors: reasons(value.errors) },
    followups: { text: followups.text, payload: followups.payload, errors: reasons(followups.errors) },
    vitals: { text: vitals.text, payload: vitals.payload, errors: reasons(vitals.errors) },
    memory: { text: memory.text, ops: memory.ops.map((op) => ({ text: op.text, scope: op.scope ?? null, pinned: op.pinned ?? null })) },
    display: stripDanglingToolCalls(display),
  };
}

describe("chat turn fixtures for the Go port", () => {
  it("matches the stored fixture", () => {
    const fixture = `${JSON.stringify(
      {
        fences: FENCES.map(fences),
        dangling: DANGLING.map((text) => ({ text, out: stripDanglingToolCalls(text) })),
        questions: QUESTIONS.map((event) => ({ event, out: needsInputText(event as never) })),
        denials: DENIALS.map((event) => ({ event, out: extractNetworkPolicyDenial(event as never) })),
        summaries: SUMMARIES.map((c) => ({ ...c, out: structuredTurnSummary(c.vitals as never, c.surfaces as never, c.notes) })),
        minutes: MINUTES.map((value) => ({ value: Number.isNaN(value) ? "NaN" : value, out: clampAnalysisMinutes(value) })),
      },
      null,
      1,
    )}\n`;
    if (process.env.UPDATE_PROMPT_FIXTURES === "1") writeFileSync(FIXTURE, fixture);
    expect(fixture).toBe(readFileSync(FIXTURE, "utf8"));
  });
});
