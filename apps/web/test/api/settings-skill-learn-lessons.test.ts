/**
 * Learning lessons and events in the admin learning settings: admins list the
 * recent events and dismiss a lesson; members see no learning observations.
 */

import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { createTestOrg, dbReachable, deleteTestOrg, uniqueOrgId } from "@neko/db/test-helpers";
import { db, eq, observation, pool } from "@neko/db";
import { callRoute } from "../_helpers/route";

const { mockGetOrgId, mockGetCurrentActor } = vi.hoisted(() => ({
  mockGetOrgId: vi.fn(),
  mockGetCurrentActor: vi.fn(),
}));

vi.mock("@/lib/db", async () => {
  const actual = await vi.importActual<typeof import("@/lib/db")>("@/lib/db");
  return { ...actual, getOrgId: mockGetOrgId };
});

vi.mock("@/lib/actor", () => ({ getCurrentActor: mockGetCurrentActor }));

const reachable = await dbReachable();
const describeIfDb = reachable ? describe : describe.skip;

if (!reachable) {
  console.warn("[api/settings/skill-learn/lessons] skipping: Postgres unreachable.");
}

describeIfDb("learning events and lessons in admin settings", () => {
  let orgId: string;
  let home: string;
  let lessonsRoute: typeof import("@/app/api/settings/skill-learn/lessons/route");
  let eventsRoute: typeof import("@/app/api/settings/skill-learn/events/route");
  let observationsRoute: typeof import("@/app/api/observations/route");

  beforeAll(async () => {
    lessonsRoute = await import("@/app/api/settings/skill-learn/lessons/route");
    eventsRoute = await import("@/app/api/settings/skill-learn/events/route");
    observationsRoute = await import("@/app/api/observations/route");
  });

  beforeEach(async () => {
    home = await mkdtemp(join(tmpdir(), "neko-lessons-home-"));
    vi.stubEnv("HOME", home);
    orgId = uniqueOrgId("api-lessons");
    await createTestOrg(orgId);
    mockGetOrgId.mockResolvedValue(orgId);
    mockGetCurrentActor.mockResolvedValue({ userId: "admin-1", role: "admin" });
  });

  afterEach(async () => {
    await deleteTestOrg(orgId);
    await rm(home, { recursive: true, force: true });
    vi.clearAllMocks();
    vi.unstubAllEnvs();
  });

  afterAll(async () => {
    await pool().end();
  });

  async function seed() {
    const [lesson] = await db()
      .insert(observation)
      .values({
        org_id: orgId,
        consumer_kind: "learning",
        title: "Aggregate order headers first.",
        body: "Aggregate order headers first.",
        dedupe_key: "lesson:org:org:abc",
        detail: {
          type: "lesson",
          targetKind: "org",
          targetId: "org",
          verdict: "promoted",
          reason: "applied grounded playbook update",
          version: "e1",
          enteredVersion: "e1",
          eventId: "e1",
          runIds: [],
          quotes: [],
          history: [],
        },
      })
      .returning({ id: observation.id });
    await db().insert(observation).values({
      org_id: orgId,
      consumer_kind: "learning_event",
      title: "Learning reached its daily limit.",
      body: "OpenNeko reached today's limit on new rules. It starts again tomorrow.",
      dedupe_key: "event:budget_hit:2026-10-16",
      detail: { type: "learning_event", kind: "budget_hit", key: "2026-10-16" },
    });
    return lesson!.id;
  }

  it("lists recent events with one line each, and only for admins", async () => {
    const lessonId = await seed();
    const res = await callRoute(eventsRoute.GET);
    expect(res.status).toBe(200);
    const events = (res.body as { events: Array<{ line: string; lessonId: string | null }> }).events;
    expect(events).toHaveLength(2);
    expect(events).toContainEqual(expect.objectContaining({ lessonId, line: "Learned a rule for org org: Aggregate order headers first." }));
    expect(events).toContainEqual(
      expect.objectContaining({
        lessonId: null,
        line: "Learning reached its daily limit. OpenNeko reached today's limit on new rules. It starts again tomorrow.",
      }),
    );

    mockGetCurrentActor.mockResolvedValue({ userId: "member-1", role: "member" });
    expect((await callRoute(eventsRoute.GET)).status).toBe(403);
    expect((await callRoute(lessonsRoute.PATCH, { method: "PATCH", body: { lessonId, status: "dismissed" } })).status).toBe(403);
    const observations = await callRoute(observationsRoute.GET, { url: "http://localhost/api/observations" });
    expect((observations.body as { observations: unknown[] }).observations).toEqual([]);
  });

  it("dismisses a lesson and blocklists its text", async () => {
    const lessonId = await seed();
    const bad = await callRoute(lessonsRoute.PATCH, { method: "PATCH", body: { lessonId } });
    expect(bad.status).toBe(400);
    const res = await callRoute(lessonsRoute.PATCH, { method: "PATCH", body: { lessonId, status: "dismissed" } });
    expect(res.status).toBe(200);
    expect(res.body).toEqual({ ok: true, dismissed: true, removedRule: false });
    const [row] = await db().select().from(observation).where(eq(observation.id, lessonId));
    expect(row).toMatchObject({ status: "dismissed", consumer_user_id: "admin-1" });

    const { readRejectedAt } = await import("../../../../packages/llm/src/work/skill-learn-playbook");
    const { learnTargetDir } = await import("@neko/llm/work");
    const { getOrgAgentRoot } = await import("../../../../packages/llm/src/work/workspace");
    expect(await readRejectedAt(learnTargetDir(getOrgAgentRoot(orgId), { kind: "org", id: "org" }))).toContain(
      "aggregate order headers first.",
    );

    const missing = await callRoute(lessonsRoute.PATCH, {
      method: "PATCH",
      body: { lessonId: "00000000-0000-0000-0000-000000000000", status: "dismissed" },
    });
    expect(missing.status).toBe(404);
  });
});
