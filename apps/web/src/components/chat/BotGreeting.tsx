"use client";

import { useEffect, useState } from "react";
import { Button } from "@/components/ui/button";
import type { BotSummary } from "@/lib/bots";
import { BotAvatar } from "./BotAvatar";

export default function BotGreeting({
  threadId,
  onPick,
}: {
  threadId: string | null;
  onPick: (text: string) => void;
}) {
  const [bot, setBot] = useState<BotSummary | null>(null);
  const [starters, setStarters] = useState<string[]>([]);

  useEffect(() => {
    if (!threadId) return;
    let cancelled = false;
    fetch(`/api/bots/thread/${threadId}`)
      .then((res) => (res.ok ? res.json() : null))
      .then((data: { bot: BotSummary | null; starters: string[] } | null) => {
        if (cancelled || !data?.bot) return;
        setBot(data.bot);
        setStarters(data.starters);
      })
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  }, [threadId]);

  if (!bot) return null;

  async function pick(job: string) {
    if (!bot) return;
    await fetch(`/api/bots/${bot.id}`, {
      method: "PATCH",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ instructions: job }),
    }).catch(() => null);
    onPick(job);
  }

  return (
    <div className="bot-greeting">
      <BotAvatar coat={bot.coat} pattern={bot.pattern} size={96} />
      <p className="bot-greeting-hello">
        Hi, I&apos;m {bot.name}.
      </p>
      {starters.length ? (
        <div className="bot-greeting-card">
          <h2 className="bot-greeting-question">What should I help with first?</h2>
          <div className="bot-greeting-options">
            {starters.map((job, index) => (
              <Button
                key={job}
                variant="secondary"
                className="bot-greeting-option"
                onClick={() => void pick(job)}
              >
                <span className="bot-greeting-letter" aria-hidden="true">
                  {String.fromCharCode(65 + index)}
                </span>
                <span>{job}</span>
              </Button>
            ))}
          </div>
          <p className="bot-greeting-hint">Or tell me in the chat below.</p>
        </div>
      ) : (
        <p className="bot-greeting-hint">
          {bot.instructions ? bot.instructions : "Tell me what to help with in the chat below."}
        </p>
      )}
    </div>
  );
}
