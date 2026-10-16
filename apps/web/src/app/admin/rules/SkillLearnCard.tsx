"use client";

import { useEffect, useState } from "react";
import { toast } from "sonner";
import { Checkbox } from "@/components/ui/checkbox";
import { Disclosure } from "@/components/ui/disclosure";
import { Badge } from "@/components/ui/badge";

type Payload = {
  enabled: boolean;
  source: "org" | "default";
};

type LearningEvent = {
  id: string;
  line: string;
  at: string;
  lessonId: string | null;
  status: string;
};

export default function SkillLearnCard() {
  const [enabled, setEnabled] = useState<boolean | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [events, setEvents] = useState<LearningEvent[]>([]);

  async function loadEvents() {
    const response = await fetch("/api/settings/skill-learn/events", { cache: "no-store" });
    if (!response.ok) return;
    const body = (await response.json()) as { events: LearningEvent[] };
    setEvents(body.events);
  }

  useEffect(() => {
    void loadEvents().catch(() => undefined);
  }, []);

  async function dismiss(lessonId: string) {
    try {
      const response = await fetch("/api/settings/skill-learn/lessons", {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ lessonId, status: "dismissed" }),
      });
      if (!response.ok) throw new Error(`HTTP ${response.status}`);
      toast.success("OpenNeko will not learn this rule again.");
      await loadEvents();
    } catch (cause) {
      toast.error(cause instanceof Error ? cause.message : "The rule could not be dismissed.");
    }
  }

  useEffect(() => {
    let cancelled = false;
    void fetch("/api/settings/skill-learn", { cache: "no-store" })
      .then(async (response) => {
        if (!response.ok) throw new Error(`HTTP ${response.status}`);
        return response.json() as Promise<Payload>;
      })
      .then((data) => {
        if (cancelled) return;
        setEnabled(data.enabled);
        setError(null);
      })
      .catch((cause: unknown) => {
        if (cancelled) return;
        setError(
          cause instanceof Error
            ? cause.message
            : "Skill learning could not be loaded.",
        );
      });
    return () => {
      cancelled = true;
    };
  }, []);

  async function onChange(next: boolean) {
    const previous = enabled;
    setEnabled(next);
    setSaving(true);
    try {
      const response = await fetch("/api/settings/skill-learn", {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ enabled: next }),
      });
      if (!response.ok) {
        const body = (await response.json().catch(() => null)) as {
          error?: string;
        } | null;
        throw new Error(body?.error ?? `HTTP ${response.status}`);
      }
      toast.success(next ? "Skill learning is on." : "Skill learning is off.");
      setError(null);
    } catch (cause) {
      setEnabled(previous);
      const message =
        cause instanceof Error
          ? cause.message
          : "Skill learning could not be saved.";
      setError(message);
      toast.error(message);
    } finally {
      setSaving(false);
    }
  }

  return (
    <section className="settings-card">
      <div className="settings-card-head">
        <div className="min-w-0">
          <h2 className="settings-card-title">Skill learning</h2>
          <p className="settings-card-copy">
            When on, OpenNeko learns from settled runs: rules for skills, notes
            for data sources, skill descriptions, and an org playbook. Authored
            skill files stay as written.
          </p>
        </div>
        {enabled === null ? null : (
          <Badge variant={enabled ? "success" : "muted"} className="self-start">
            {enabled ? "On" : "Off"}
          </Badge>
        )}
      </div>
      {error ? (
        <p className="mb-3 text-ui-body-sm text-danger">{error}</p>
      ) : null}
      {enabled === null && !error ? (
        <p className="text-ui-body-sm text-text3">Loading…</p>
      ) : (
        <Checkbox
          label="Learn from skill use"
          checked={enabled === true}
          disabled={saving || enabled === null}
          onCheckedChange={(checked) => void onChange(checked === true)}
        />
      )}
      {events.length > 0 ? (
        <div className="mt-4">
          <h3 className="m-0 mb-2 text-ui-body-sm font-medium text-text">Recent learning events</h3>
          <ul className="m-0 flex list-none flex-col gap-1.5 p-0">
            {events.map((event) => (
              <li key={event.id} className="flex items-start justify-between gap-3 text-ui-body-sm text-text2">
                <span className="min-w-0 break-words">
                  <time className="mr-2 text-text3" dateTime={event.at}>
                    {event.at.slice(0, 10)}
                  </time>
                  {event.line}
                </span>
                {event.lessonId && event.status !== "dismissed" ? (
                  <button
                    type="button"
                    className="shrink-0 text-ui-body-sm text-text3 underline hover:text-text"
                    onClick={() => void dismiss(event.lessonId!)}
                  >
                    Dismiss
                  </button>
                ) : event.status === "dismissed" ? (
                  <span className="shrink-0 text-text3">Dismissed</span>
                ) : null}
              </li>
            ))}
          </ul>
        </div>
      ) : null}
      <Disclosure title="What this changes" className="mt-4">
        <p className="m-0 text-ui-body-sm leading-[1.55] text-text2">
          OpenNeko writes a learned overlay after the same lesson shows up in
          enough runs: LEARNED.md and a description for a skill, NOTES.md for a
          data source, and PLAYBOOK.md for the org (at most 8 rules). Overlays
          do not store customer data. Each target has its own decision log of
          applies, skips, rejects, and rollbacks. Off by default.
        </p>
      </Disclosure>
    </section>
  );
}
