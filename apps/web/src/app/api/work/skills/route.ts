import { NextResponse } from "next/server";
import { getOrgId } from "@/lib/db";
import { listWorkSkills } from "@/lib/work-files";

export async function GET() {
  const skills = await listWorkSkills(await getOrgId());
  return NextResponse.json({ skills });
}
