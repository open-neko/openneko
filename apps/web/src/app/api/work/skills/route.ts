import { NextResponse } from "next/server";
import { getOrgId } from "@/lib/db";
import { filterToHeld } from "@/lib/entitlements";
import { listWorkSkills } from "@/lib/work-files";

export async function GET() {
  const skills = await filterToHeld("skill", await listWorkSkills(await getOrgId()), (skill) => skill.name);
  return NextResponse.json({ skills });
}
