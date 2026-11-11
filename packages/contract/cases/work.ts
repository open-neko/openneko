import { expect } from "vitest";
import { body, type Case } from "../src/case";
import { storedZip } from "../src/zip";

const SKILL = `---
name: contract-skill
description: Use when the contract tests check the skill routes.
---

# Contract skill

Say hello.
`;

function skillArchive(): FormData {
  const form = new FormData();
  form.set("file", new Blob([storedZip({ "contract-skill/SKILL.md": SKILL })], { type: "application/zip" }), "contract-skill.zip");
  return form;
}

function upload(threadId: string): FormData {
  const form = new FormData();
  form.set("threadId", threadId);
  form.set("file", new Blob(["sku,stock\n1001,2\n"], { type: "text/csv" }), "stock.csv");
  return form;
}

const run = (c: { ids: Record<string, string> }) => ({ threadId: c.ids.adminThreadId!, runId: c.ids.adminWorkRunId! });

export const workCases: Case[] = [
  {
    op: "GET /work/threads", name: "lists the member's threads only", as: "member", status: 200,
    check: (r, c) => expect(JSON.stringify(body(r))).not.toContain(c.ids.adminThreadId),
  },
  {
    op: "POST /work/threads", name: "starts a thread", status: 200, request: { body: { title: "Contract chat" } },
    check: (r, c) => {
      c.ids.threadId = String((body(r).thread as { id: string }).id);
    },
  },
  { op: "GET /work/threads/{threadId}", name: "reads the thread", status: 200, request: (c) => ({ params: { threadId: c.ids.threadId } }) },
  { op: "GET /work/threads/{threadId}", name: "hides another user's thread", as: "member", status: 404, request: (c) => ({ params: { threadId: c.ids.threadId } }) },
  { op: "POST /work/threads/{threadId}/runs", name: "needs a message", status: 400, request: (c) => ({ params: { threadId: c.ids.threadId }, body: {} }) },
  { op: "POST /work/threads/{threadId}/runs", name: "refuses an unknown thread", status: 404, request: (c) => ({ params: { threadId: c.ids.unknownId }, body: { message: "Hello" } }) },
  { op: "POST /work/threads/{threadId}/truncate", name: "refuses an unknown message", status: 404, request: (c) => ({ params: { threadId: c.ids.threadId }, body: { messageId: c.ids.unknownId } }) },
  { op: "POST /work/upload", name: "needs a multipart body", status: 400, request: { body: {} } },
  { op: "POST /work/upload", name: "refuses an unknown thread", status: 404, request: (c) => ({ form: upload(c.ids.unknownId!) }) },
  { op: "DELETE /work/threads/{threadId}", name: "deletes the thread", status: 200, request: (c) => ({ params: { threadId: c.ids.threadId } }) },
  { op: "DELETE /work/threads/{threadId}", name: "refuses a deleted thread", status: 404, request: (c) => ({ params: { threadId: c.ids.threadId } }) },
  { op: "GET /work/threads/{threadId}/runs/{runId}/events", name: "streams a finished run", status: 200, request: (c) => ({ params: run(c) }) },
  { op: "GET /work/threads/{threadId}/runs/{runId}/events", name: "hides another user's run", as: "member", status: 404, request: (c) => ({ params: run(c) }) },
  { op: "POST /work/threads/{threadId}/runs/{runId}/timing", name: "records the timing", status: 204, request: (c) => ({ params: run(c), body: { doneMs: 1200 } }) },
  { op: "POST /work/threads/{threadId}/runs/{runId}/timing", name: "refuses an unknown run", status: 404, request: (c) => ({ params: { ...run(c), runId: c.ids.unknownId! }, body: { doneMs: 1 } }) },
  {
    op: "POST /work/threads/{threadId}/runs/{runId}/approve-action", name: "needs an action request", status: 400,
    request: (c) => ({ params: run(c), body: {} }),
  },
  { op: "GET /work/runs/{runId}/events", name: "lists the run events", status: 200, request: (c) => ({ params: { runId: c.ids.adminWorkRunId } }) },
  { op: "GET /work/runs/{runId}/events", name: "hides another user's run", as: "member", status: 404, request: (c) => ({ params: { runId: c.ids.adminWorkRunId } }) },
  { op: "POST /work/runs/{runId}/cancel", name: "refuses an unknown run", status: 404, request: (c) => ({ params: { runId: c.ids.unknownId } }) },
  { op: "GET /work/memories", name: "lists the memories", status: 200 },
  { op: "GET /work/memories/pending", name: "lists the pending memories", status: 200 },
  { op: "GET /work/memories/pull", name: "lists the team updates", as: "member", status: 200 },
  { op: "POST /work/memories/pull", name: "applies no decisions", as: "member", status: 200, request: { body: { decisions: [] } } },
  { op: "POST /work/memories/pull", name: "is for members", status: 403, request: { body: { decisions: [] } } },
  { op: "GET /work/memories/{id}", name: "refuses an unknown memory", status: 404, request: (c) => ({ params: { id: c.ids.unknownId } }) },
  { op: "PATCH /work/memories/{id}", name: "refuses an unknown memory", as: "member", status: 404, request: (c) => ({ params: { id: c.ids.unknownId }, body: { suppress: true } }) },
  { op: "DELETE /work/memories/{id}", name: "refuses an unknown memory", status: 404, request: (c) => ({ params: { id: c.ids.unknownId }, body: {} }) },
  { op: "POST /work/memories/{id}/promote", name: "refuses an unknown memory", status: 404, request: (c) => ({ params: { id: c.ids.unknownId } }) },
  { op: "POST /work/memories/decide", name: "refuses an unknown memory", status: 404, request: (c) => ({ body: { id: c.ids.unknownId, action: "decline" } }) },
  { op: "POST /work/memories/decide", name: "needs a memory", status: 400, request: { body: {} } },
  { op: "GET /work/skills", name: "lists the skills", status: 200 },
  {
    op: "POST /work/skills", name: "installs a skill archive", status: 201, request: { form: skillArchive() },
    check: (r) => expect(body(r).name).toBe("contract-skill"),
  },
  { op: "POST /work/skills", name: "needs an archive", status: 400, request: { body: {} } },
  { op: "GET /work/skills/{name}", name: "reads the skill", status: 200, request: { params: { name: "contract-skill" } } },
  { op: "GET /work/skills/{name}/file", name: "reads the skill file", status: 200, request: { params: { name: "contract-skill" }, query: { path: "SKILL.md" } } },
  { op: "GET /work/skills/{name}/file", name: "needs a path", status: 400, request: { params: { name: "contract-skill" } } },
  {
    op: "PUT /work/skills/{name}/file", name: "saves the skill file", status: 200,
    request: { params: { name: "contract-skill" }, query: { path: "SKILL.md" }, body: { content: SKILL.replace("Say hello.", "Say hello twice.") } },
  },
  { op: "PUT /work/skills/{name}/file", name: "refuses an unknown skill", status: 404, request: { params: { name: "contract-none" }, query: { path: "SKILL.md" }, body: { content: "x" } } },
  { op: "DELETE /work/skills/{name}", name: "deletes the skill", status: 200, request: { params: { name: "contract-skill" } } },
  { op: "GET /work/skills/{name}", name: "refuses a deleted skill", status: 404, request: { params: { name: "contract-skill" } } },
  { op: "DELETE /work/skills/{name}", name: "refuses a deleted skill", status: 404, request: { params: { name: "contract-skill" } } },
  { op: "GET /work/assets", name: "lists the assets", status: 200 },
  { op: "GET /work/files/{path}", name: "serves only run artifacts", status: 404, request: { params: { path: "skills/graphjin-config/SKILL.md" } } },
  { op: "GET /work/files/{path}", name: "refuses an artifact the run did not emit", status: 404, request: (c) => ({ params: { path: `runs/${c.ids.adminWorkRunId}/artifacts/none.csv` } }) },
  { op: "GET /work/audit/export", name: "exports the audit log", status: 200 },
  { op: "GET /work/audit/export", name: "is for admins", as: "member", status: 403 },
  { op: "GET /work/config/history", name: "lists the config history", status: 200 },
  { op: "POST /work/config/restore", name: "needs a commit and a path", status: 400, request: { body: {} } },
];
