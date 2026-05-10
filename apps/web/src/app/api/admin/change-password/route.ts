/**
 * POST /api/admin/change-password
 *
 * Bootstraps the admin's chosen DB password on first run. Steps:
 *   1. Run `ALTER USER neko WITH PASSWORD '<new>'` against the live DB
 *      (we're already authenticated as `neko` via the bootstrap default).
 *   2. Persist the new password to `~/.config/openneko/config.json` so the next
 *      process boot picks it up.
 *   3. Drain the web's pool so subsequent queries use the new password.
 *   4. Fire-and-forget POST /admin/reconnect to the worker so its
 *      pg-boss singleton (which holds the OLD credentials in its own
 *      pool, separate from the web's @neko/db pool) restarts with fresh
 *      creds. Tolerates the worker being down — the password still
 *      rotates successfully on the web side.
 *
 * The body must contain a non-empty password. The default `"secret"` is
 * rejected — we want a real change. Length minimum is intentionally
 * loose (8 chars) since this is a host-local DB password, not a public
 * credential; the operator picks the policy.
 */

import { NextRequest, NextResponse } from "next/server";
import { db, reconnectPool, sql, writeLocalConfig } from "@neko/db";

const MIN_LENGTH = 8;
const FORBIDDEN = new Set(["secret", "password", "postgres"]);

function isPlainPasswordSafe(password: string): boolean {
  // ALTER USER ... PASSWORD '...' takes a plain-text literal. Postgres
  // hashes it server-side. We refuse single quotes and backslashes so
  // the string can't escape the literal context. Most password managers
  // generate alnum + symbol passwords without these characters.
  return !/['\\\n\r\0]/.test(password);
}

export async function POST(request: NextRequest) {
  try {
    const body = (await request.json().catch(() => ({}))) as { password?: unknown };
    const password = typeof body.password === "string" ? body.password.trim() : "";

    if (password.length < MIN_LENGTH) {
      return NextResponse.json(
        { error: `Password must be at least ${MIN_LENGTH} characters.` },
        { status: 400 },
      );
    }
    if (FORBIDDEN.has(password.toLowerCase())) {
      return NextResponse.json(
        { error: "That password is too common. Pick something else." },
        { status: 400 },
      );
    }
    if (!isPlainPasswordSafe(password)) {
      return NextResponse.json(
        { error: "Password contains characters we can't safely apply (quotes, backslashes, newlines)." },
        { status: 400 },
      );
    }

    // ALTER USER takes a literal; we vetted the string above so a sql.raw
    // is safe here. Drizzle's parameter binding won't work for DDL.
    await db().execute(sql.raw(`alter user neko with password '${password}'`));

    writeLocalConfig({ pg: { password } });

    // Drain the pool so subsequent queries use the new password.
    await reconnectPool();

    // Tell the worker process to restart so its pg-boss singleton
    // (created at boot with old creds) gets rebuilt. Best-effort —
    // the worker may not be running yet during /setup, and that's fine.
    try {
      await fetch("http://127.0.0.1:4100/admin/reconnect", {
        method: "POST",
        signal: AbortSignal.timeout(2000),
      });
    } catch {
      // worker down / not yet listening / network blip — non-fatal
    }

    return NextResponse.json({ ok: true });
  } catch (e) {
    const message = e instanceof Error ? e.message : String(e);
    return NextResponse.json({ error: message }, { status: 500 });
  }
}

/**
 * GET /api/admin/change-password — small status endpoint so the wizard
 * knows whether the password has already been changed (config file
 * has pg.password) without leaking the value.
 */
export async function GET() {
  const { hasCustomPassword } = await import("@neko/db");
  return NextResponse.json({ changed: hasCustomPassword() });
}
