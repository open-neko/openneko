import { readFileSync } from "node:fs";
import pg from "pg";
import { expect, test } from "@playwright/test";
import { FIXTURE as F } from "../../../../packages/contract/src/fixtures";

const AR_DETAIL_ID = "c0a7ac70-0000-4000-8000-000000000451";
const AR_QUEUE_ID = "c0a7ac70-0000-4000-8000-000000000452";
const AR_PLAIN_ID = "c0a7ac70-0000-4000-8000-000000000453";
const AR_TOAST_ID = "c0a7ac70-0000-4000-8000-000000000454";

function phoneDbUrl(): string {
  if (process.env.OPENNEKO_DATABASE_URL) return process.env.OPENNEKO_DATABASE_URL;
  if (process.env.OPENNEKO_DATABASE_URL_FILE) {
    return readFileSync(process.env.OPENNEKO_DATABASE_URL_FILE, "utf8").trim();
  }
  throw new Error("OPENNEKO_DATABASE_URL or OPENNEKO_DATABASE_URL_FILE is required");
}

function localhostOrigin(baseURL: string | undefined): string {
  if (!baseURL) throw new Error("baseURL is required");
  return baseURL.replace("://127.0.0.1", "://localhost");
}

test.describe("4.5 passkey action approval on the phone", () => {
  test.use({ viewport: { width: 400, height: 860 } });

  test("approves actions on a 400 px phone screen with a WebAuthn virtual authenticator and records passkey details in the audit log", async ({
    page,
    baseURL,
  }) => {
    const dbUrl = phoneDbUrl();
    const client = new pg.Client({ connectionString: dbUrl });
    await client.connect();
    try {
      const {
        rows: [{ id: orgId }],
      } = await client.query<{ id: string }>(
        "select id from organization order by created_at asc limit 1",
      );
      await client.query(`delete from app_passkey where org_id = $1`, [orgId]);
      await client.query(
        `insert into action_request (id, org_id, workflow_run_id, scope, kind, target, summary, payload, status)
         values
           ($1, $3, $4, 'org', 'note', 'warehouse-eu-west', 'Hold inbound shipment #451 for inspection', '{"lot":"LOT-451","hold":true}'::jsonb, 'pending_approval'),
           ($2, $3, $4, 'org', 'note', 'warehouse-eu-west', 'Release safety stock batch #452', '{"lot":"LOT-452","release":true}'::jsonb, 'pending_approval'),
           ($5, $3, $4, 'org', 'note', 'warehouse-eu-west', 'Recount bin A-453', '{"bin":"A-453"}'::jsonb, 'pending_approval'),
           ($6, $3, $4, 'org', 'note', 'warehouse-eu-west', 'Move pallet #454 to quarantine', '{"pallet":"454"}'::jsonb, 'pending_approval')
         on conflict (id) do update
           set status = 'pending_approval',
               approved_by_user_id = null,
               approved_at = null,
               rejection_reason = null`,
        [AR_DETAIL_ID, AR_QUEUE_ID, orgId, F.adminWorkflowRunId, AR_PLAIN_ID, AR_TOAST_ID],
      );

      const cdp = await page.context().newCDPSession(page);
      await cdp.send("WebAuthn.enable");
      const { authenticatorId } = await cdp.send("WebAuthn.addVirtualAuthenticator", {
        options: {
          protocol: "ctap2",
          transport: "internal",
          hasResidentKey: true,
          hasUserVerification: true,
          isUserVerified: true,
          automaticPresenceSimulation: true,
        },
      });

      const origin = localhostOrigin(baseURL);
      const pageErrors: string[] = [];
      const apiCalls: string[] = [];
      page.on("pageerror", (err) => pageErrors.push(err.message));
      page.on("response", async (res) => {
        if (res.url().includes("/api/v1/") && res.status() >= 400) {
          apiCalls.push(`${res.request().method()} ${res.url()} -> ${res.status()} ${await res.text().catch(() => "")}`);
        }
      });

      // 1. Open the action detail screen on a 400 px phone viewport and approve with passkey.
      // Because the user has no passkey yet, this enrolls a passkey on the virtual authenticator
      // and then signs the approval challenge with it.
      await page.goto(`${origin}/actions/${AR_DETAIL_ID}`, { waitUntil: "networkidle" });
      await expect(
        page.getByRole("heading", { name: "Hold inbound shipment #451 for inspection" }),
      ).toBeVisible();
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= 400)).toBe(true);
      expect(await page.evaluate(() => window.scrollX)).toBe(0);

      await page.getByRole("button", { name: "Approve with passkey", exact: true }).click();
      await expect
        .poll(async () => {
          if (apiCalls.length > 0 || pageErrors.length > 0) {
            return `ERR: ${apiCalls.join(" | ")} ${pageErrors.join(" | ")}`;
          }
          const r = await client.query<{ status: string }>(
            `select status from action_request where id = $1`,
            [AR_DETAIL_ID],
          );
          return r.rows[0]?.status;
        })
        .toBe("approved");

      const { credentials } = await cdp.send("WebAuthn.getCredentials", { authenticatorId });
      expect(credentials.length).toBe(1);
      expect(credentials[0]?.signCount).toBeGreaterThanOrEqual(1);

      // 2. Open the Approvals queue (/actions) at 400 px width and approve the second action
      // using the already-registered passkey on the virtual authenticator.
      await page.goto(`${origin}/actions`, { waitUntil: "networkidle" });
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= 400)).toBe(true);
      expect(await page.evaluate(() => window.scrollX)).toBe(0);

      const secondRow = page.locator("li", {
        hasText: "Release safety stock batch #452",
      });
      await expect(secondRow).toBeVisible();
      await secondRow.getByRole("button", { name: "Approve with passkey", exact: true }).click();

      await expect
        .poll(async () => {
          const r = await client.query<{ status: string }>(
            `select status from action_request where id = $1`,
            [AR_QUEUE_ID],
          );
          return r.rows[0]?.status;
        })
        .toBe("approved");

      const status = async (id: string) =>
        (await client.query<{ status: string }>(`select status from action_request where id = $1`, [id])).rows[0]
          ?.status;

      // F3b: with a passkey registered, the plain Approve button gets 403 passkey_required,
      // and the client then signs the approval challenge with the passkey.
      await page.goto(`${origin}/actions/${AR_PLAIN_ID}`, { waitUntil: "networkidle" });
      await expect(page.getByRole("heading", { name: "Recount bin A-453" })).toBeVisible();
      await page.getByRole("button", { name: "Approve", exact: true }).click();
      await expect.poll(() => status(AR_PLAIN_ID)).toBe("approved");

      // On the queue, the approval waits for the undo window; the 403 then shows a notice
      // with a button that asks for the passkey.
      await page.goto(`${origin}/actions`, { waitUntil: "networkidle" });
      const toastRow = page.locator("li", { hasText: "Move pallet #454 to quarantine" });
      await expect(toastRow).toBeVisible();
      await toastRow.getByRole("button", { name: "Approve", exact: true }).click();
      await expect(page.getByText("This approval needs your passkey")).toBeVisible({ timeout: 15000 });
      expect(await status(AR_TOAST_ID)).toBe("pending_approval");
      await page.getByRole("button", { name: "Use passkey", exact: true }).click();
      await expect.poll(() => status(AR_TOAST_ID)).toBe("approved");

      // 3. Check /profile at 400 px phone width shows the registered passkey with no sideways scroll.
      await page.goto(`${origin}/profile`, { waitUntil: "networkidle" });
      await expect(page.getByRole("heading", { name: "Passkeys", exact: true })).toBeVisible();
      await expect(page.getByText("This device")).toBeVisible();
      await expect(page.getByText("Used for approval")).toBeVisible();
      await expect(page.getByTestId("passkey-approval-rule")).toHaveText(
        "After you add a passkey, each approval in the browser asks for it.",
      );
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= 400)).toBe(true);
      expect(await page.evaluate(() => window.scrollX)).toBe(0);
      expect(pageErrors).toEqual([]);

      // 4. Verify the database passkey record and the audit_chain entries for both approvals.
      const passkeys = await client.query<{
        credential_id: string;
        name: string;
        sign_count: string;
        has_used: boolean;
      }>(
        `select credential_id, name, sign_count::text, (last_used_at is not null) as has_used
         from app_passkey where org_id = $1`,
        [orgId],
      );
      expect(passkeys.rows).toHaveLength(1);
      const pk = passkeys.rows[0]!;
      expect(pk.name).toBe("This device");
      expect(Number(pk.sign_count)).toBeGreaterThanOrEqual(2);
      expect(pk.has_used).toBe(true);

      for (const actionId of [AR_DETAIL_ID, AR_QUEUE_ID, AR_PLAIN_ID, AR_TOAST_ID]) {
        const audit = await client.query<{
          event: string;
          payload: {
            approvedBy: string;
            kind: string;
            method: string;
            passkeyCredentialId: string;
            passkeyName: string;
            soloOperator: boolean;
            userVerified: boolean;
          };
          payload_hash: string;
          chain_hash: string;
        }>(
          `select event, payload, payload_hash, chain_hash
           from audit_chain
           where org_id = $1 and entity_kind = 'action_request' and entity_id = $2
           order by seq desc limit 1`,
          [orgId, actionId],
        );
        expect(audit.rows).toHaveLength(1);
        const row = audit.rows[0]!;
        expect(row.event).toBe("approved");
        expect(row.payload).toMatchObject({
          kind: "note",
          method: "passkey",
          passkeyCredentialId: pk.credential_id,
          passkeyName: "This device",
          soloOperator: false,
          userVerified: true,
        });
        expect(row.payload_hash).toMatch(/^[0-9a-f]{64}$/);
        expect(row.chain_hash).toMatch(/^[0-9a-f]{64}$/);
      }

      // 5. Removing the last passkey asks for the passkey first.
      await page.goto(`${origin}/profile`, { waitUntil: "networkidle" });
      await page.getByRole("button", { name: "Remove", exact: true }).click();
      await expect
        .poll(async () => (await client.query(`select 1 from app_passkey where org_id = $1`, [orgId])).rowCount)
        .toBe(0);
      expect(pageErrors).toEqual([]);
    } finally {
      await client
        .query(`delete from action_request where id in ($1, $2, $3, $4)`, [AR_DETAIL_ID, AR_QUEUE_ID, AR_PLAIN_ID, AR_TOAST_ID])
        .catch(() => {});
      await client.query(`delete from app_passkey`).catch(() => {});
      await client.end();
    }
  });
});
