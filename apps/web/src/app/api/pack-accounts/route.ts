import { NextResponse } from "next/server";
import { getCurrentUser } from "@/lib/auth";
import { isDenied, requireAdminActor } from "@/lib/admin-auth";
import { getCurrentActor } from "@/lib/actor";
import { requestPackWorker } from "@/lib/solution-packs";

export async function GET() {
  const user = await getCurrentUser();
  if (!user) { const actor = await requireAdminActor(); if (isDenied(actor)) return actor; }
  try {
    const result = await requestPackWorker("/admin/pack-accounts", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ owner: user ? `user:${user.id}` : "solo" }) });
    return NextResponse.json({ ...(result.body as object), canConfigure: (await getCurrentActor()).role === "admin" }, { status: result.status });
  } catch { return NextResponse.json({ error: "Pack accounts could not be loaded" }, { status: 502 }); }
}
