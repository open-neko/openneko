import "server-only";

import { pool } from "@neko/db";

const PUBLIC_URL_SCOPE = "public-url";

export type PublicUrlSetting = {
  saved: string | null;
  environment: string | null;
};

export async function getPublicUrlSetting(orgId: string): Promise<PublicUrlSetting> {
  const { rows } = await pool().query<{ url: string | null }>(
    "select config->>'url' as url from llm_provider_config where org_id = $1 and scope = $2",
    [orgId, PUBLIC_URL_SCOPE],
  );
  const environment = process.env.OPENNEKO_PUBLIC_URL?.trim().replace(/\/+$/, "") || null;
  return { saved: rows[0]?.url || null, environment };
}
