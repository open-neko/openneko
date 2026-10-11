import { and, bot, db, eq } from "@neko/db";

export type ThreadBot = { id: string; name: string; instructions: string };

export const BOT_INSTRUCTIONS_MAX = 2000;

/** The bot that owns this chat, if the chat belongs to one. */
export async function getThreadBot(orgId: string, threadId: string): Promise<ThreadBot | null> {
  const [row] = await db()
    .select({ id: bot.id, name: bot.name, instructions: bot.instructions })
    .from(bot)
    .where(and(eq(bot.org_id, orgId), eq(bot.thread_id, threadId)))
    .limit(1);
  return row ?? null;
}

export async function updateThreadBotInstructions(input: {
  orgId: string;
  threadId: string;
  instructions: string;
}): Promise<{ ok: boolean }> {
  const rows = await db()
    .update(bot)
    .set({
      instructions: input.instructions.trim().slice(0, BOT_INSTRUCTIONS_MAX),
      updated_at: new Date(),
    })
    .where(and(eq(bot.org_id, input.orgId), eq(bot.thread_id, input.threadId)))
    .returning({ id: bot.id });
  return { ok: rows.length > 0 };
}
