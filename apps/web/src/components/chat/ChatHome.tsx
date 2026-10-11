"use client";

import Link from "next/link";
import { Plus, Star } from "lucide-react";
import { ButtonLink } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty";
import { LocalDateTime } from "@/components/ui/local-date-time";
import { SkeletonList } from "@/components/ui/skeleton";
import { BotAvatar } from "./BotAvatar";
import { useBots } from "./useBots";

export default function ChatHome() {
  const { bots, failed } = useBots();

  return (
    <main className="chat-home">
      <div className="chat-home-head">
        <h1 className="chat-home-title">Bots</h1>
        <ButtonLink href="/chat/bots/new" variant="primary">
          <Plus aria-hidden="true" strokeWidth={2} />
          New bot
        </ButtonLink>
      </div>

      {failed ? (
        <EmptyState title="Bots did not load" body="Reload the page to try again." />
      ) : bots === null ? (
        <SkeletonList rows={3} variant="row" label="Loading bots" />
      ) : (
        <ul className="chat-list">
          {bots.map((bot) => (
            <li key={bot.id}>
              <Link href={bot.threadId ? `/chat/t/${bot.threadId}` : "/chat"} className="chat-row">
                <BotAvatar
                  coat={bot.coat}
                  pattern={bot.pattern}
                  size={44}
                  animated={false}
                  aria-hidden
                  badge={
                    bot.isDefault ? (
                      <span className="chat-row-badge">
                        <Star aria-hidden="true" strokeWidth={2.5} />
                      </span>
                    ) : null
                  }
                />
                <span className="chat-row-copy">
                  <span className="chat-row-title">{bot.name}</span>
                  {bot.lastMessageAt ? (
                    <LocalDateTime value={bot.lastMessageAt} className="chat-row-time" />
                  ) : null}
                </span>
              </Link>
            </li>
          ))}
        </ul>
      )}
    </main>
  );
}
