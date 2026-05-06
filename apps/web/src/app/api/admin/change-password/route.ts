/**
 * POST /api/admin/change-password
 *
 * Bootstraps the admin's chosen DB password on first run. Steps:
 *   1. Run `ALTER USER neko WITH PASSWORD '<new>'` against the live DB
 *      (we're already authenticated as `neko` via the bootstrap default).
 *   2. Persist the new password to `~/.config/neko/config.json` so the next
 *      process boot picks it up.
 *   3. Drain the pool so the in-process app reconnects with the new
 *      password without a restart.
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
