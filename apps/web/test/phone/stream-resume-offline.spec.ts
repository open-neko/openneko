import { randomUUID } from "node:crypto";
import { readFileSync } from "node:fs";
import { expect, test } from "@playwright/test";
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
});
