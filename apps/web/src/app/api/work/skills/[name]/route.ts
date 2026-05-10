import { NextResponse } from "next/server";
import { getOrgId } from "@/lib/db";
import { deleteWorkSkill, getWorkSkillDetail } from "@/lib/work-files";

type RouteContext = {
  params: Promise<{ name: string }>;
};

export async function GET(_: Request, context: RouteContext) {
  const { name } = await context.params;
  const skill = await getWorkSkillDetail(await getOrgId(), decodeURIComponent(name));
  if (!skill) {
    return NextResponse.json({ error: "Skill not found" }, { status: 404 });
  }
  return NextResponse.json({ skill });
}

export async function DELETE(_: Request, context: RouteContext) {
  const { name } = await context.params;
  const ok = await deleteWorkSkill(await getOrgId(), decodeURIComponent(name));
  if (!ok) {
    return NextResponse.json({ error: "Skill not found" }, { status: 404 });
  }
  return NextResponse.json({ ok: true });
}
