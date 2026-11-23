import { randomUUID } from "node:crypto";
import { readFileSync } from "node:fs";
import { expect, test, type BrowserContext, type Page } from "@playwright/test";
import pg from "pg";

function resolveDatabaseUrl(): string {
  const file = process.env.OPENNEKO_DATABASE_URL_FILE;
  if (file) {
    return readFileSync(file, "utf8").trim();
  }
  if (process.env.DATABASE_URL) {
    return process.env.DATABASE_URL;
  }
  throw new Error("OPENNEKO_DATABASE_URL_FILE or DATABASE_URL is required");
}

async function emitRunEvent(
  client: pg.Client,
  orgId: string,
  threadId: string,
  runId: string,
  event: Record<string, unknown>,
): Promise<number> {
  const kind = typeof event.type === "string" ? event.type : null;
  const { rows } = await client.query<{ id: string }>(
    `insert into work_run_event (org_id, thread_id, run_id, kind, payload)
     values ($1, $2, $3, $4, $5::jsonb)
     returning id::text as id`,
    [orgId, threadId, runId, kind, JSON.stringify(event)],
  );
  await client.query(`select pg_notify('work_run_event', $1)`, [runId]);
  return Number(rows[0]!.id);
}

async function completeRunInDb(
  client: pg.Client,
  orgId: string,
  threadId: string,
  runId: string,
  finalText: string,
): Promise<void> {
  await client.query(
    `insert into work_message (org_id, thread_id, run_id, role, content)
     values ($1, $2, $3, 'assistant', $4)`,
    [orgId, threadId, runId, finalText],
  );
  await client.query(
    `update work_run
     set status = 'completed', finished_at = now(), updated_at = now()
     where id = $1`,
    [runId],
  );
  await emitRunEvent(client, orgId, threadId, runId, {
    type: "done",
    result: { status: "completed", minutesSaved: 0 },
  });
}

test.describe("4.6 stream resume and offline question queue", () => {
  test.use({ viewport: { width: 400, height: 860 } });

  test("resumes an in-flight answer across reload and after a lost connection, and queues offline questions with waiting state and cancel", async ({
    page,
  }) => {
    const client = new pg.Client({ connectionString: resolveDatabaseUrl() });
    await client.connect();
    try {
      const { rows: orgRows } = await client.query<{
        id: string;
        solo_admin_user_id: string | null;
      }>("select id, solo_admin_user_id from organization limit 1");
      const orgId = orgRows[0]!.id;
      let userId = orgRows[0]!.solo_admin_user_id;
      if (!userId) {
        const { rows: userRows } = await client.query<{ id: string }>(
          "select id from app_user where org_id = $1 order by created_at asc limit 1",
          [orgId],
        );
        userId = userRows[0]!.id;
      }

      const threadId = randomUUID();
      await client.query(
        `insert into work_thread (id, org_id, title, created_by_user_id)
         values ($1, $2, 'Stream and offline check', $3)`,
        [threadId, orgId, userId],
      );

      const startedRuns: Array<{ runId: string; message: string }> = [];
      const eventStreamUrls: string[] = [];

      page.on("request", (req) => {
        if (req.url().includes(`/api/v1/work/threads/${threadId}/runs/`)) {
          eventStreamUrls.push(req.url());
        }
      });

      await page.route(
        `**/api/v1/work/threads/${threadId}/runs`,
        async (route) => {
          if (route.request().method() !== "POST") {
            await route.continue();
            return;
          }
          const body = JSON.parse(route.request().postData() ?? "{}") as {
            message?: string;
          };
          const message = (body.message ?? "").trim();
          const runId = randomUUID();
          await client.query(
            `insert into work_run (id, org_id, thread_id, backend, status, actor_user_id, actor_role)
             values ($1, $2, $3, 'ax', 'running', $4, 'admin')`,
            [runId, orgId, threadId, userId],
          );
          await client.query(
            `insert into work_message (org_id, thread_id, run_id, role, content)
             values ($1, $2, $3, 'user', $4)`,
            [orgId, threadId, runId, message],
          );
          startedRuns.push({ runId, message });
          await route.fulfill({
            status: 200,
            contentType: "application/json",
            body: JSON.stringify({
              runId,
              threadId,
              backend: "ax",
              actorRole: "admin",
            }),
          });
        },
      );

      await page.goto(`/work/${threadId}`, { waitUntil: "networkidle" });
      const composer = page.locator("textarea.work-input");
      await expect(composer).toBeVisible();

      // 1. Reload in the middle of an answer picks the answer up where the server is.
      await composer.fill("Explain Q4 margin movement");
      await composer.press("Enter");

      await expect.poll(() => startedRuns.length).toBe(1);
      const reloadRunId = startedRuns[0]!.runId;

      await emitRunEvent(client, orgId, threadId, reloadRunId, {
        type: "message",
        role: "assistant",
        content: "Gross margin rose 140 bps in Q4, ",
      });
      await expect(page.locator(".work-transcript")).toContainText(
        "Gross margin rose 140 bps in Q4,",
      );

      // Server advances while the page reloads in the middle of the answer.
      await emitRunEvent(client, orgId, threadId, reloadRunId, {
        type: "message",
        role: "assistant",
        content: "driven by lower freight rates, ",
      });

      await page.reload({ waitUntil: "domcontentloaded" });

      // Immediately picks up where the server is (including the second chunk).
      await expect(page.locator(".work-transcript")).toContainText(
        "Gross margin rose 140 bps in Q4, driven by lower freight rates,",
      );

      // Server finishes the answer after the reload.
      await emitRunEvent(client, orgId, threadId, reloadRunId, {
        type: "message",
        role: "assistant",
        content: "with net margin reaching 19.2%.",
      });
      await completeRunInDb(
        client,
        orgId,
        threadId,
        reloadRunId,
        "Gross margin rose 140 bps in Q4, driven by lower freight rates, with net margin reaching 19.2%.",
      );

      const fullReloadAnswer =
        "Gross margin rose 140 bps in Q4, driven by lower freight rates, with net margin reaching 19.2%.";
      await expect(page.locator(".work-transcript")).toContainText(
        fullReloadAnswer,
      );
      const reloadTranscriptText =
        (await page.locator(".work-transcript").textContent()) ?? "";
      expect(
        reloadTranscriptText.split("Gross margin rose 140 bps in Q4,").length -
          1,
      ).toBe(1);

      // 2. Lost connection in the middle of an answer picks up where the server is.
      await expect(composer).toBeEnabled();
      await composer.fill("Break down warehouse fulfillment latency");
      await composer.press("Enter");

      await expect.poll(() => startedRuns.length).toBe(2);
      const dropRunId = startedRuns[1]!.runId;

      const firstChunkId = await emitRunEvent(
        client,
        orgId,
        threadId,
        dropRunId,
        {
          type: "message",
          role: "assistant",
          content: "West warehouse averages 1.2 days, ",
        },
      );
      await expect(page.locator(".work-transcript")).toContainText(
        "West warehouse averages 1.2 days,",
      );

      // Lose connection in the middle of the answer.
      await page.context().setOffline(true);
      await expect(page.getByTestId("offline-banner")).toBeVisible();

      // Server emits the next part of the answer while the browser has no connection.
      await emitRunEvent(client, orgId, threadId, dropRunId, {
        type: "message",
        role: "assistant",
        content: "Central warehouse averages 1.8 days, ",
      });

      // Restore connection; browser must resume from firstChunkId and pick up the server's progress.
      await page.context().setOffline(false);
      await expect(page.getByTestId("offline-banner")).toHaveCount(0);

      await expect
        .poll(() =>
          eventStreamUrls.some((url) =>
            url.includes(`/runs/${dropRunId}/events?afterId=${firstChunkId}`),
          ),
        )
        .toBe(true);

      await expect(page.locator(".work-transcript")).toContainText(
        "West warehouse averages 1.2 days, Central warehouse averages 1.8 days,",
      );

      await emitRunEvent(client, orgId, threadId, dropRunId, {
        type: "message",
        role: "assistant",
        content: "and East warehouse averages 0.9 days.",
      });
      await completeRunInDb(
        client,
        orgId,
        threadId,
        dropRunId,
        "West warehouse averages 1.2 days, Central warehouse averages 1.8 days, and East warehouse averages 0.9 days.",
      );

      const fullDropAnswer =
        "West warehouse averages 1.2 days, Central warehouse averages 1.8 days, and East warehouse averages 0.9 days.";
      await expect(page.locator(".work-transcript")).toContainText(
        fullDropAnswer,
      );
      const dropTranscriptText =
        (await page.locator(".work-transcript").textContent()) ?? "";
      expect(
        dropTranscriptText.split("West warehouse averages 1.2 days,").length -
          1,
      ).toBe(1);

      // 3. A question typed offline waits with a visible "waiting" state and a way to cancel,
      //    and a remaining waiting question sends once when the network is back.
      await expect(composer).toBeEnabled();
      await page.context().setOffline(true);
      await expect(page.getByTestId("offline-banner")).toBeVisible();

      await composer.fill("Question to cancel while offline");
      await composer.press("Enter");

      const queuedItem = page.getByTestId("offline-queued-question");
      const waitingStatus = page.getByTestId("offline-waiting-status");
      await expect(queuedItem).toContainText("Question to cancel while offline");
      await expect(waitingStatus).toContainText(
        "Waiting for network — will send once online",
      );
      expect(startedRuns).toHaveLength(2);

      // Cancel the offline question before the network returns.
      await waitingStatus.getByRole("button", { name: /cancel/i }).click();
      await expect(queuedItem).toHaveCount(0);
      await expect(waitingStatus).toHaveCount(0);

      // Type a second offline question and let it wait for the network.
      await composer.fill("Question that sends once when online");
      await composer.press("Enter");

      await expect(queuedItem).toContainText(
        "Question that sends once when online",
      );
      await expect(waitingStatus).toContainText(
        "Waiting for network — will send once online",
      );
      expect(startedRuns).toHaveLength(2);

      // Restore the network: the waiting question sends once and streams its answer.
      await page.context().setOffline(false);
      await expect(page.getByTestId("offline-banner")).toHaveCount(0);
      await expect(waitingStatus).toHaveCount(0);

      await expect.poll(() => startedRuns.length).toBe(3);
      expect(startedRuns[2]!.message).toBe(
        "Question that sends once when online",
      );
      expect(
        startedRuns.some(
          (item) => item.message === "Question to cancel while offline",
        ),
      ).toBe(false);

      const offlineRunId = startedRuns[2]!.runId;
      await emitRunEvent(client, orgId, threadId, offlineRunId, {
        type: "message",
        role: "assistant",
        content: "Sent once after reconnecting and answered in full.",
      });
      await completeRunInDb(
        client,
        orgId,
        threadId,
        offlineRunId,
        "Sent once after reconnecting and answered in full.",
      );

      await expect(page.locator(".work-transcript")).toContainText(
        "Sent once after reconnecting and answered in full.",
      );
      expect(startedRuns).toHaveLength(3);
    } finally {
      await page.context().setOffline(false).catch(() => {});
      await client.end();
    }
  });

  test("sends each offline question once: a lost response, a closed tab during the send, two tabs, a busy and a refusing server, and storage per user", async ({
    context,
    page: firstPage,
  }) => {
    let page = firstPage;
    const client = new pg.Client({ connectionString: resolveDatabaseUrl() });
    await client.connect();
    try {
      const { rows: orgRows } = await client.query<{ id: string }>(
        "select id from organization limit 1",
      );
      const orgId = orgRows[0]!.id;
      const session = (await (
        await page.request.get("/api/v1/auth/session")
      ).json()) as { user: { id: string } | null };
      const userId = session.user!.id;
      const threadId = randomUUID();
      await client.query(
        `insert into work_thread (id, org_id, title, created_by_user_id)
         values ($1, $2, 'Offline send once check', $3)`,
        [threadId, orgId, userId],
      );

      type Post = { page: string; message: string; clientRequestId: string };
      const posts: Post[] = [];
      let onPost: (route: import("@playwright/test").Route, post: Post) => Promise<boolean> =
        async () => false;
      const watch = async (p: import("@playwright/test").Page, name: string) => {
        await p.route(`**/api/v1/work/threads/${threadId}/runs`, async (route) => {
          if (route.request().method() !== "POST") return route.continue();
          const body = JSON.parse(route.request().postData() ?? "{}") as {
            message?: string;
            clientRequestId?: string;
          };
          const post = {
            page: name,
            message: body.message ?? "",
            clientRequestId: body.clientRequestId ?? "",
          };
          posts.push(post);
          if (!(await onPost(route, post))) await route.continue();
        });
      };
      const runsFor = async (message: string) => {
        const { rows } = await client.query<{ runs: string; ids: string; messages: string }>(
          `select count(distinct r.id)::text as runs,
                  count(distinct r.client_request_id)::text as ids,
                  count(m.id)::text as messages
             from work_message m join work_run r on r.id = m.run_id
            where m.thread_id = $1 and m.role = 'user' and m.content = $2`,
          [threadId, message],
        );
        return rows[0]!;
      };
      const ask = async (p: import("@playwright/test").Page, text: string) => {
        const composer = p.locator("textarea.work-input");
        await expect(composer).toBeEnabled({ timeout: 20_000 });
        await composer.fill(text);
        await composer.press("Enter");
        await expect(p.getByTestId("offline-queued-question")).toContainText(text);
      };
      const goOffline = async () => {
        await expect(page.locator("textarea.work-input")).toBeEnabled({ timeout: 20_000 });
        await context.setOffline(true);
      };
      const storedKeys = (p: import("@playwright/test").Page) =>
        p.evaluate(() =>
          Object.keys(localStorage).filter((k) => k.startsWith("openneko.work.offlineQueue")),
        );

      await watch(page, "A");
      const sessionLoaded = page.waitForResponse((r) => r.url().includes("/api/v1/auth/session") && r.ok());
      await page.goto(`/work/${threadId}`, { waitUntil: "networkidle" });
      await sessionLoaded;

      // 1. The server makes the run but the response is lost; the resend gets the same run.
      await context.setOffline(true);
      await ask(page, "Lost response question");
      await expect.poll(() => storedKeys(page)).toEqual([`openneko.work.offlineQueue.user.${userId}`]);
      onPost = async (route, post) => {
        if (post.message !== "Lost response question") return false;
        onPost = async () => false;
        await route.fetch();
        await route.abort("connectionreset");
        return true;
      };
      await context.setOffline(false);
      await expect
        .poll(() => posts.filter((p) => p.message === "Lost response question").length, { timeout: 20_000 })
        .toBe(2);
      const lost = posts.filter((p) => p.message === "Lost response question");
      expect(lost[0]!.clientRequestId).toMatch(/^[A-Za-z0-9._-]{1,128}$/);
      expect(lost[1]!.clientRequestId).toBe(lost[0]!.clientRequestId);
      await expect(page.getByTestId("offline-queued-question")).toHaveCount(0);
      expect(await runsFor("Lost response question")).toEqual({ runs: "1", ids: "1", messages: "1" });

      // 2. The question stays stored while it sends, so a closed or reloaded tab loses nothing.
      await goOffline();
      await ask(page, "Reload during send question");
      let held = false;
      onPost = async (_route, post) => {
        if (post.message !== "Reload during send question") return false;
        onPost = async () => false;
        held = true;
        return true;
      };
      await context.setOffline(false);
      await expect.poll(() => held, { timeout: 20_000 }).toBe(true);
      const next = await context.newPage();
      await watch(next, "C");
      await next.goto(`/work/${threadId}`, { waitUntil: "domcontentloaded" });
      const stored = await next.evaluate(
        (key) => localStorage.getItem(key) ?? "",
        `openneko.work.offlineQueue.user.${userId}`,
      );
      expect(stored).toContain("Reload during send question");
      expect(stored).not.toContain("claimedBy");
      await page.close({ runBeforeUnload: false });
      page = next;
      // Playwright may let the held request of the closing tab through; then
      // that tab completes the question. Else the new tab sends it again.
      await expect
        .poll(async () => (await runsFor("Reload during send question")).runs, { timeout: 20_000 })
        .toBe("1");
      await expect(page.getByTestId("offline-queued-question")).toHaveCount(0);
      const reloaded = posts.filter((p) => p.message === "Reload during send question");
      expect(reloaded[0]!.page).toBe("A");
      if (reloaded.length > 1) {
        expect(reloaded.map((p) => p.page)).toEqual(["A", "C"]);
        expect(reloaded[1]!.clientRequestId).toBe(reloaded[0]!.clientRequestId);
      }
      expect(await runsFor("Reload during send question")).toEqual({ runs: "1", ids: "1", messages: "1" });
      await expect(page.getByTestId("offline-queued-question")).toHaveCount(0);

      // 3. Two tabs on one thread: one tab sends the question, once.
      const second = await context.newPage();
      await watch(second, "B");
      await second.goto(`/work/${threadId}`, { waitUntil: "networkidle" });
      await goOffline();
      await ask(page, "Two tabs question");
      await expect(second.getByTestId("offline-queued-question")).toContainText("Two tabs question");
      await context.setOffline(false);
      await expect.poll(async () => (await runsFor("Two tabs question")).runs, { timeout: 20_000 }).toBe("1");
      await expect(page.getByTestId("offline-queued-question")).toHaveCount(0);
      await expect(second.getByTestId("offline-queued-question")).toHaveCount(0);
      expect(posts.filter((p) => p.message === "Two tabs question")).toHaveLength(1);
      expect(await runsFor("Two tabs question")).toEqual({ runs: "1", ids: "1", messages: "1" });
      await second.close();

      // 4. A busy server keeps the question and sends it again; a refusal stays on the question.
      await goOffline();
      await ask(page, "Busy server question");
      let busy = 0;
      onPost = async (route, post) => {
        if (post.message !== "Busy server question") return false;
        busy++;
        if (busy > 1) return false;
        await route.fulfill({
          status: 503,
          contentType: "application/json",
          headers: { "Retry-After": "5" },
          body: JSON.stringify({ error: "busy" }),
        });
        return true;
      };
      await context.setOffline(false);
      await expect.poll(() => busy, { timeout: 20_000 }).toBe(1);
      await expect(page.getByTestId("offline-queued-question")).toContainText("Busy server question");
      await expect
        .poll(async () => (await runsFor("Busy server question")).runs, { timeout: 30_000 })
        .toBe("1");
      await expect(page.getByTestId("offline-queued-question")).toHaveCount(0);
      expect(posts.filter((p) => p.message === "Busy server question")).toHaveLength(2);

      await goOffline();
      await ask(page, "Refused question");
      onPost = async (route, post) => {
        if (post.message !== "Refused question") return false;
        await route.fulfill({
          status: 413,
          contentType: "application/json",
          body: JSON.stringify({ error: "too large" }),
        });
        return true;
      };
      await context.setOffline(false);
      await expect(page.getByTestId("offline-failed-status")).toContainText("HTTP 413");
      await page.waitForTimeout(2_000);
      expect(posts.filter((p) => p.message === "Refused question")).toHaveLength(1);
      await page.getByRole("button", { name: "Send refused question again" }).click();
      await expect.poll(() => posts.filter((p) => p.message === "Refused question").length, { timeout: 20_000 }).toBe(2);
      await expect(page.getByTestId("offline-failed-status")).toContainText("HTTP 413");
      await page.getByRole("button", { name: "Remove refused question" }).click();
      await expect(page.getByTestId("offline-queued-question")).toHaveCount(0);
      onPost = async () => false;

      // 5. Sign-out removes the stored questions of this browser, of every user.
      await goOffline();
      await ask(page, "Question left at sign-out");
      let parked = false;
      onPost = async (_route, post) => {
        if (post.message !== "Question left at sign-out") return false;
        parked = true;
        return true;
      };
      await page.evaluate(() => {
        localStorage.setItem("openneko.work.offlineQueue.user.someone-else", "[]");
      });
      await context.setOffline(false);
      await expect.poll(() => parked, { timeout: 20_000 }).toBe(true);
      await page.route("**/api/v1/auth/logout", (route) =>
        route.fulfill({ status: 200, contentType: "application/json", body: "{}" }),
      );
      expect(await storedKeys(page)).toContain(`openneko.work.offlineQueue.user.${userId}`);
      await page.getByRole("navigation", { name: "Primary navigation" }).getByRole("button", { name: "More" }).click();
      await page.getByRole("button", { name: /sign out/i }).click();
      await page.waitForURL(/\/signin/);
      expect(await storedKeys(page)).toEqual([]);
    } finally {
      await context.setOffline(false).catch(() => {});
      await client.end();
    }
  });

  test("sends nothing of user A from a stale tab after user B signs in, backs off on a refused new thread, and flushes in solo mode (FIX15)", async ({
    context,
    page,
  }) => {
    const client = new pg.Client({ connectionString: resolveDatabaseUrl() });
    await client.connect();
    try {
      const { rows: orgRows } = await client.query<{ id: string }>(
        "select id from organization limit 1",
      );
      const orgId = orgRows[0]!.id;
      const session = (await (
        await page.request.get("/api/v1/auth/session")
      ).json()) as { user: { id: string } | null; authEnabled: boolean };
      expect(session.authEnabled).toBe(false);
      const userA = session.user!.id;
      const threadId = randomUUID();
      await client.query(
        `insert into work_thread (id, org_id, title, created_by_user_id)
         values ($1, $2, 'Stale tab check', $3)`,
        [threadId, orgId, userA],
      );
      const runsFor = async (message: string) => {
        const { rows } = await client.query<{ runs: string }>(
          `select count(distinct m.run_id)::text as runs from work_message m
            where m.thread_id = $1 and m.role = 'user' and m.content = $2`,
          [threadId, message],
        );
        return rows[0]!.runs;
      };
      const posted: string[] = [];
      await context.route(`**/api/v1/work/threads/${threadId}/runs`, async (route) => {
        if (route.request().method() === "POST") {
          posted.push((JSON.parse(route.request().postData() ?? "{}") as { message?: string }).message ?? "");
        }
        await route.continue();
      });
      const ask = async (p: import("@playwright/test").Page, text: string) => {
        const composer = p.locator("textarea.work-input");
        await expect(composer).toBeEnabled({ timeout: 20_000 });
        await composer.fill(text);
        await composer.press("Enter");
        await expect(p.getByTestId("offline-queued-question")).toContainText(text);
      };
      const goOffline = async (p: import("@playwright/test").Page) => {
        await expect(p.locator("textarea.work-input")).toBeEnabled({ timeout: 20_000 });
        await context.setOffline(true);
      };
      const keyA = `openneko.work.offlineQueue.user.${userA}`;

      await page.goto(`/work/${threadId}`, { waitUntil: "networkidle" });

      // Solo mode: the session names the solo admin, and a queued question sends once.
      await goOffline(page);
      await ask(page, "Solo mode question");
      await context.setOffline(false);
      await expect.poll(() => runsFor("Solo mode question"), { timeout: 20_000 }).toBe("1");
      await expect(page.getByTestId("offline-queued-question")).toHaveCount(0);

      // User A's session ends and user B signs in from another tab.
      await goOffline(page);
      await ask(page, "Private question of user A");
      await context.route("**/api/v1/auth/session", (route) =>
        route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify({
            user: { id: "user-b", email: "b@example.com", name: null },
            role: "member",
            authEnabled: true,
            signedIn: true,
          }),
        }),
      );
      await context.setOffline(false);
      const tabB = await context.newPage();
      await tabB.goto(`/work/${threadId}`, { waitUntil: "networkidle" });
      await expect(page.getByTestId("offline-queued-question")).toHaveCount(0, { timeout: 20_000 });
      await page.waitForTimeout(3_000);
      await expect(tabB.getByTestId("offline-queued-question")).toHaveCount(0);
      expect(posted.filter((m) => m === "Private question of user A")).toEqual([]);
      expect(await runsFor("Private question of user A")).toBe("0");
      expect(await page.evaluate((key) => localStorage.getItem(key), keyA)).toBeNull();
      await tabB.close();

      // User A signs in again: the question of user A left the browser when user B was confirmed.
      await context.unroute("**/api/v1/auth/session");
      await page.reload({ waitUntil: "networkidle" });
      await page.waitForTimeout(3_000);
      expect(await runsFor("Private question of user A")).toBe("0");
      await expect(page.getByTestId("offline-queued-question")).toHaveCount(0);

      // A refused new thread waits for its retry time and then fails; it does not resend each second.
      let creates = 0;
      await context.route("**/api/v1/work/threads", async (route) => {
        if (route.request().method() !== "POST") return route.continue();
        creates++;
        await route.fulfill(
          creates === 1
            ? {
                status: 503,
                contentType: "application/json",
                headers: { "Retry-After": "5" },
                body: JSON.stringify({ error: "busy" }),
              }
            : { status: 403, contentType: "application/json", body: JSON.stringify({ error: "not allowed" }) },
        );
      });
      await page.goto("/work", { waitUntil: "networkidle" });
      await goOffline(page);
      await ask(page, "New thread question");
      await context.setOffline(false);
      await expect.poll(() => creates, { timeout: 20_000 }).toBe(1);
      await page.waitForTimeout(3_000);
      expect(creates).toBe(1);
      await expect(page.getByTestId("offline-waiting-status")).toBeVisible();
      await expect(page.getByTestId("offline-failed-status")).toContainText("HTTP 403", { timeout: 20_000 });
      await page.waitForTimeout(3_000);
      expect(creates).toBe(2);
      await page.getByRole("button", { name: "Remove refused question" }).click();
      await expect(page.getByTestId("offline-queued-question")).toHaveCount(0);
    } finally {
      await context.setOffline(false).catch(() => {});
      await client.end();
    }
  });

  test("sends for the signed-in user and waits out a run in flight, online and from the queue (FIX18)", async ({
    context,
    page,
  }) => {
    const client = new pg.Client({ connectionString: resolveDatabaseUrl() });
    await client.connect();
    try {
      const { rows: orgRows } = await client.query<{ id: string }>(
        "select id from organization limit 1",
      );
      const orgId = orgRows[0]!.id;
      const session = (await (
        await page.request.get("/api/v1/auth/session")
      ).json()) as { user: { id: string } | null };
      const userA = session.user!.id;
      const threadId = randomUUID();
      await client.query(
        `insert into work_thread (id, org_id, title, created_by_user_id)
         values ($1, $2, 'Run in flight check', $3)`,
        [threadId, orgId, userA],
      );
      const runsFor = async (message: string) => {
        const { rows } = await client.query<{ runs: string }>(
          `select count(distinct m.run_id)::text as runs from work_message m
            where m.thread_id = $1 and m.role = 'user' and m.content = $2`,
          [threadId, message],
        );
        return rows[0]!.runs;
      };
      const posts: { message?: string; expectedUserId?: string }[] = [];
      const busyOnce = new Set<string>();
      await context.route(`**/api/v1/work/threads/${threadId}/runs`, async (route) => {
        if (route.request().method() !== "POST") return route.continue();
        const body = JSON.parse(route.request().postData() ?? "{}") as { message?: string; expectedUserId?: string };
        posts.push(body);
        if (body.message && !busyOnce.has(body.message)) {
          busyOnce.add(body.message);
          return route.fulfill({
            status: 409,
            contentType: "application/json",
            headers: { "Retry-After": "1" },
            body: JSON.stringify({ error: "A question is still running in this thread.", code: "run_in_flight", runId: randomUUID() }),
          });
        }
        return route.continue();
      });
      const composer = page.locator("textarea.work-input");

      await page.goto(`/work/${threadId}`, { waitUntil: "networkidle" });
      await expect(composer).toBeEnabled({ timeout: 20_000 });
      await composer.fill("Online while a run ends");
      await composer.press("Enter");
      await expect.poll(() => runsFor("Online while a run ends"), { timeout: 20_000 }).toBe("1");
      expect(posts.filter((p) => p.message === "Online while a run ends")).toHaveLength(2);

      await expect(composer).toBeEnabled({ timeout: 20_000 });
      await context.setOffline(true);
      await composer.fill("Queued while a run ends");
      await composer.press("Enter");
      await expect(page.getByTestId("offline-queued-question")).toContainText("Queued while a run ends");
      await context.setOffline(false);
      await expect.poll(() => posts.filter((p) => p.message === "Queued while a run ends").length, { timeout: 20_000 }).toBe(1);
      await expect(page.getByTestId("offline-waiting-status")).toBeVisible();
      await expect(page.getByTestId("offline-failed-status")).toHaveCount(0);
      await expect.poll(() => runsFor("Queued while a run ends"), { timeout: 30_000 }).toBe("1");
      await expect(page.getByTestId("offline-queued-question")).toHaveCount(0);

      expect(posts.length).toBe(4);
      expect(posts.every((p) => p.expectedUserId === userA)).toBe(true);
    } finally {
      await context.setOffline(false).catch(() => {});
      await client.end();
    }
  });

  test("queues a question typed offline while a run streams and sends new-chat questions from another thread in order (FIX18b)", async ({
    context,
    page,
  }) => {
    const client = new pg.Client({ connectionString: resolveDatabaseUrl() });
    await client.connect();
    try {
      const { rows: orgRows } = await client.query<{ id: string }>(
        "select id from organization limit 1",
      );
      const orgId = orgRows[0]!.id;
      const session = (await (
        await page.request.get("/api/v1/auth/session")
      ).json()) as { user: { id: string } | null };
      const userA = session.user!.id;
      const threadId = randomUUID();
      await client.query(
        `insert into work_thread (id, org_id, title, created_by_user_id)
         values ($1, $2, 'Offline while streaming', $3)`,
        [threadId, orgId, userA],
      );
      const liveRun = async (thread: string) => {
        const { rows } = await client.query<{ id: string }>(
          `select id::text from work_run where thread_id = $1 and status in ('queued', 'running')`,
          [thread],
        );
        return rows[0]?.id ?? null;
      };
      const userMessages = async (content: string[]) => {
        const { rows } = await client.query<{ thread_id: string; content: string }>(
          `select thread_id::text, content from work_message
            where role = 'user' and content = any($1) order by created_at, id`,
          [content],
        );
        return rows;
      };
      const composer = page.locator("textarea.work-input");

      // A run streams; the network goes; the composer still takes a question and queues it.
      const runId = randomUUID();
      await client.query(
        `insert into work_run (id, org_id, thread_id, backend, status, actor_user_id, actor_role)
         values ($1, $2, $3, 'ax', 'running', $4, 'admin')`,
        [runId, orgId, threadId, userA],
      );
      await client.query(
        `insert into work_message (org_id, thread_id, run_id, role, content)
         values ($1, $2, $3, 'user', 'Question that started a run')`,
        [orgId, threadId, runId],
      );
      await page.goto(`/work/${threadId}`, { waitUntil: "domcontentloaded" });
      await expect(page.getByRole("button", { name: "Stop" })).toBeVisible({ timeout: 20_000 });
      await expect(composer).toBeDisabled();
      await context.setOffline(true);
      await expect(composer).toBeEnabled({ timeout: 20_000 });
      await composer.fill("Typed offline while the run streams");
      await composer.press("Enter");
      await expect(page.getByTestId("offline-queued-question")).toContainText("Typed offline while the run streams");
      await context.setOffline(false);
      await page.waitForTimeout(4_000);
      expect(await liveRun(threadId)).toBe(runId);
      expect(await userMessages(["Typed offline while the run streams"])).toEqual([]);
      await expect(page.getByTestId("offline-queued-question")).toContainText("Typed offline while the run streams");
      await completeRunInDb(client, orgId, threadId, runId, "The first run is done.");
      await expect
        .poll(async () => (await userMessages(["Typed offline while the run streams"])).length, { timeout: 30_000 })
        .toBe(1);
      await expect(page.getByTestId("offline-queued-question")).toHaveCount(0);
      const second = await liveRun(threadId);
      if (second) await completeRunInDb(client, orgId, threadId, second, "Done.");

      // Two questions typed in a new chat wait while the user opens another thread.
      const now = new Date().toISOString();
      await page.evaluate(
        ({ key, items }) => localStorage.setItem(key, JSON.stringify(items)),
        {
          key: `openneko.work.offlineQueue.user.${userA}`,
          items: [
            { id: randomUUID(), threadId: null, threadBase: "/work", content: "First new-chat question", createdAt: now, status: "waiting" },
            { id: randomUUID(), threadId: null, threadBase: "/work", content: "Second new-chat question", createdAt: now, status: "waiting" },
          ],
        },
      );
      await page.reload({ waitUntil: "networkidle" });
      await expect.poll(async () => (await userMessages(["First new-chat question"])).length, { timeout: 20_000 }).toBe(1);
      const [{ thread_id: newThread }] = await userMessages(["First new-chat question"]);
      expect(newThread).not.toBe(threadId);
      await expect
        .poll(async () => (await userMessages(["Second new-chat question"])).length, { timeout: 30_000 })
        .toBe(1);
      expect(await userMessages(["First new-chat question", "Second new-chat question"])).toEqual([
        { thread_id: newThread, content: "First new-chat question" },
        { thread_id: newThread, content: "Second new-chat question" },
      ]);
      expect(page.url()).toContain(`/work/${threadId}`);
      await expect
        .poll(() => page.evaluate((key) => localStorage.getItem(key), `openneko.work.offlineQueue.user.${userA}`))
        .toBeNull();
    } finally {
      await context.setOffline(false).catch(() => {});
      await client.end();
    }
  });

  test("sends three questions queued offline in one thread in order, each streaming live after the run before it ends (FIX20)", async ({
    context,
    page,
  }) => {
    await checkQueuedAfterRun(context, page, "three");
  });

  test("streams a question queued during a live run when the thread reload after that run is slow (FIX20)", async ({
    context,
    page,
  }) => {
    await checkQueuedAfterRun(context, page, "live");
  });

  test("shows no failure notice when the server answers a queued question with run_in_flight (FIX20)", async ({
    context,
    page,
  }) => {
    await checkQueuedAfterRun(context, page, "busy");
  });
});

test.describe("4.6 offline question notices, Retry and the send timeout (FIX20)", () => {
  test.use({ viewport: { width: 400, height: 860 } });

  test("tells once how many questions were removed, links refused questions of other chats, sends again on Retry and after a send that hangs", async ({
    context,
    page,
  }) => {
    test.setTimeout(150_000);
    const client = new pg.Client({ connectionString: resolveDatabaseUrl() });
    await client.connect();
    try {
      const { rows: orgRows } = await client.query<{ id: string }>(
        "select id from organization limit 1",
      );
      const orgId = orgRows[0]!.id;
      const session = (await (
        await page.request.get("/api/v1/auth/session")
      ).json()) as { user: { id: string } | null };
      const userA = session.user!.id;
      const [here, there] = [randomUUID(), randomUUID()];
      for (const id of [here, there]) {
        await client.query(
          `insert into work_thread (id, org_id, title, created_by_user_id)
           values ($1, $2, 'Notice check', $3)`,
          [id, orgId, userA],
        );
      }
      const sent = async (content: string) => {
        const { rows } = await client.query<{ n: string }>(
          `select count(*)::text as n from work_message where role = 'user' and content = $1`,
          [content],
        );
        return rows[0]!.n;
      };
      const myKey = `openneko.work.offlineQueue.user.${userA}`;
      const now = new Date().toISOString();
      const old = new Date(Date.now() - 8 * 24 * 60 * 60 * 1000).toISOString();

      // 3. Questions removed unsent show one notice with the count.
      await page.goto(`/work/${here}`, { waitUntil: "networkidle" });
      await page.evaluate(
        ({ myKey, otherKey, items, others }) => {
          localStorage.setItem(myKey, JSON.stringify(items));
          localStorage.setItem(otherKey, JSON.stringify(others));
        },
        {
          myKey,
          otherKey: "openneko.work.offlineQueue.user.someone-else",
          items: [
            { id: randomUUID(), threadId: there, threadBase: "/work", content: "Eight days old", createdAt: old, status: "waiting" },
            { id: randomUUID(), threadId: there, threadBase: "/work", content: "Refused in the other chat", createdAt: now, status: "failed", error: "The server refused this question (HTTP 400): bad" },
          ],
          others: [
            { id: randomUUID(), threadId: null, threadBase: "/work", content: "Not mine 1", createdAt: now, status: "waiting" },
            { id: randomUUID(), threadId: null, threadBase: "/work", content: "Not mine 2", createdAt: now, status: "waiting" },
          ],
        },
      );
      await page.reload({ waitUntil: "networkidle" });
      const lost = page.getByTestId("offline-lost-notice");
      await expect(lost).toContainText("1 question that waited for the network was not sent and removed after 7 days.");
      await expect(lost).toContainText("2 questions that another user typed in this browser were not sent and removed.");
      await lost.getByRole("button", { name: "Dismiss" }).click();
      await expect(lost).toHaveCount(0);
      await page.reload({ waitUntil: "networkidle" });
      await expect(page.getByTestId("offline-queued-question")).toHaveCount(0);
      await expect(lost).toHaveCount(0);

      // 6. A refused question of another chat shows a count and a link to it.
      const elsewhere = page.getByTestId("offline-failed-elsewhere");
      await expect(elsewhere).toContainText("The server refused 1 question in other chats.");
      await elsewhere.getByRole("link").click();
      await expect(page).toHaveURL(new RegExp(`/work/${there}$`));
      await expect(page.getByTestId("offline-failed-status")).toContainText("HTTP 400");
      await expect(elsewhere).toHaveCount(0);

      // 7. Retry sends the refused question again.
      await page.getByRole("button", { name: "Send refused question again" }).click();
      await expect.poll(() => sent("Refused in the other chat"), { timeout: 20_000 }).toBe("1");
      await expect(page.getByTestId("offline-queued-question")).toHaveCount(0);

      // 7. A queued send that hangs ends at the send timeout and goes again.
      let posts = 0;
      let release: () => void = () => {};
      const released = new Promise<void>((resolve) => {
        release = resolve;
      });
      await context.route(`**/api/v1/work/threads/${here}/runs`, async (route) => {
        if (route.request().method() !== "POST") return route.continue();
        posts += 1;
        if (posts === 1) {
          await released;
          return route.abort().catch(() => {});
        }
        return route.continue();
      });
      await page.goto(`/work/${here}`, { waitUntil: "networkidle" });
      const composer = page.locator("textarea.work-input");
      await expect(composer).toBeEnabled({ timeout: 20_000 });
      await context.setOffline(true);
      await composer.fill("Sent after a hung post");
      await composer.press("Enter");
      await context.setOffline(false);
      await expect.poll(() => posts, { timeout: 20_000 }).toBe(1);
      const hungAt = Date.now();
      await expect.poll(() => posts, { timeout: 60_000 }).toBe(2);
      expect(Date.now() - hungAt).toBeGreaterThan(25_000);
      await expect.poll(() => sent("Sent after a hung post"), { timeout: 20_000 }).toBe("1");
      await expect(page.getByTestId("offline-queued-question")).toHaveCount(0);
      release();
    } finally {
      await context.setOffline(false).catch(() => {});
      await client.end();
    }
  });
});

async function checkQueuedAfterRun(
  context: BrowserContext,
  page: Page,
  mode: "three" | "live" | "busy",
) {
  const liveRunFirst = mode !== "three";
  const client = new pg.Client({ connectionString: resolveDatabaseUrl() });
  await client.connect();
  try {
    const { rows: orgRows } = await client.query<{ id: string }>(
      "select id from organization limit 1",
    );
    const orgId = orgRows[0]!.id;
    const session = (await (
      await page.request.get("/api/v1/auth/session")
    ).json()) as { user: { id: string } | null };
    const userA = session.user!.id;
    const threadId = randomUUID();
    await client.query(
      `insert into work_thread (id, org_id, title, created_by_user_id)
       values ($1, $2, 'Three queued questions', $3)`,
      [threadId, orgId, userA],
    );
    const runs: { runId: string; message: string }[] = [];
    let busyAnswers = 0;
    await context.route(`**/api/v1/work/threads/${threadId}/runs`, async (route) => {
      if (route.request().method() !== "POST") return route.continue();
      const body = JSON.parse(route.request().postData() ?? "{}") as { message?: string };
      if (mode === "busy" && busyAnswers === 0) {
        busyAnswers += 1;
        return route.fulfill({
          status: 409,
          contentType: "application/json",
          headers: { "Retry-After": "1" },
          body: JSON.stringify({ error: "A question is still running in this thread.", code: "run_in_flight" }),
        });
      }
      const runId = randomUUID();
      await client.query(
        `insert into work_run (id, org_id, thread_id, backend, status, actor_user_id, actor_role)
         values ($1, $2, $3, 'ax', 'running', $4, 'admin')`,
        [runId, orgId, threadId, userA],
      );
      await client.query(
        `insert into work_message (org_id, thread_id, run_id, role, content)
         values ($1, $2, $3, 'user', $4)`,
        [orgId, threadId, runId, body.message ?? ""],
      );
      runs.push({ runId, message: body.message ?? "" });
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ runId, threadId, backend: "ax", actorRole: "admin" }),
      });
    });
    // A slow thread read returns a bundle from before the next queued send.
    await context.route(
      (url) => url.pathname === `/api/v1/work/threads/${threadId}`,
      async (route) => {
        if (route.request().method() !== "GET") return route.continue();
        const response = await route.fetch();
        await new Promise((resolve) => setTimeout(resolve, 1_500));
        return route.fulfill({ response });
      },
    );
    const questions = liveRunFirst
      ? ["Queued during a live run"]
      : ["Queued question one", "Queued question two", "Queued question three"];
    const composer = page.locator("textarea.work-input");
    const liveRunId = randomUUID();
    if (liveRunFirst) {
      await client.query(
        `insert into work_run (id, org_id, thread_id, backend, status, actor_user_id, actor_role)
         values ($1, $2, $3, 'ax', 'running', $4, 'admin')`,
        [liveRunId, orgId, threadId, userA],
      );
      await client.query(
        `insert into work_message (org_id, thread_id, run_id, role, content)
         values ($1, $2, $3, 'user', 'Question of the live run')`,
        [orgId, threadId, liveRunId],
      );
    }
    await page.goto(`/work/${threadId}`, { waitUntil: liveRunFirst ? "domcontentloaded" : "networkidle" });
    if (liveRunFirst) {
      await expect(page.getByRole("button", { name: "Stop" })).toBeVisible({ timeout: 20_000 });
    } else {
      await expect(composer).toBeEnabled({ timeout: 20_000 });
    }
    await context.setOffline(true);
    for (const question of questions) {
      await expect(composer).toBeEnabled({ timeout: 20_000 });
      await composer.fill(question);
      await composer.press("Enter");
    }
    await expect(page.getByTestId("offline-queued-question")).toHaveCount(questions.length);
    await context.setOffline(false);
    if (liveRunFirst) {
      await page.waitForTimeout(3_000);
      expect(runs).toHaveLength(0);
      await completeRunInDb(client, orgId, threadId, liveRunId, "The live run is done.");
    }
    if (mode === "busy") {
      await expect.poll(() => busyAnswers, { timeout: 20_000 }).toBe(1);
      await expect(page.getByTestId("offline-waiting-status")).toContainText("Server busy");
      for (let i = 0; i < 5; i++) {
        expect(await page.locator('.work-transcript > [role="alert"]').count()).toBe(0);
        await page.waitForTimeout(200);
      }
    }

    const transcript = page.locator(".work-transcript");
    for (let i = 0; i < questions.length; i++) {
      await expect.poll(() => runs.length, { timeout: 30_000 }).toBe(i + 1);
      expect(runs.map((run) => run.message)).toEqual(questions.slice(0, i + 1));
      await expect(page.getByRole("button", { name: "Stop" })).toBeVisible();
      await page.waitForTimeout(2_000);
      const answer = `Answer number ${i + 1}`;
      await emitRunEvent(client, orgId, threadId, runs[i]!.runId, {
        type: "message",
        role: "assistant",
        content: answer,
      });
      await expect(transcript).toContainText(answer, { timeout: 10_000 });
      await completeRunInDb(client, orgId, threadId, runs[i]!.runId, answer);
    }
    await expect(page.getByTestId("offline-queued-question")).toHaveCount(0);
    await expect(page.getByRole("button", { name: "Stop" })).toHaveCount(0, { timeout: 20_000 });
    await page.waitForTimeout(3_000);
    expect(runs).toHaveLength(questions.length);
    for (let i = 0; i < questions.length; i++) {
      await expect(transcript).toContainText(`Answer number ${i + 1}`);
    }
  } finally {
    await context.setOffline(false).catch(() => {});
    await client.end();
  }
}
