/**
 * GET /api/plugins/action-descriptors — the operator-facing snapshot
 * of every installed plugin's declared action kinds + seeded default
 * mode. Used by /settings/rules to render an "Installed plugins"
 * section that surfaces what kinds the agent can call.
 *
 * Thin proxy over the worker admin endpoint
 * (/admin/plugins/action-descriptors) — same 2s timeout, same
 * graceful-empty-list fallback so a temporarily-unreachable worker
 * doesn't break the rules page.
 */

import { NextResponse } from "next/server";
import { getPluginActionDescriptors } from "@/lib/auth";

export async function GET() {
  const descriptors = await getPluginActionDescriptors();
  return NextResponse.json({ descriptors });
}
