"use client";

import { useCallback, useEffect, useState, type FormEvent } from "react";
import { adminApi } from "@/components/admin/admin-api";
import { ActionGroup } from "@/components/ui/action-group";
import { Button } from "@/components/ui/button";
import { Field, NativeSelect, Textarea } from "@/components/ui/field";

export type ReplyMode = "thread" | "direct" | "listed";
export type ReplyChannel = "slack" | "telegram";
export type ReplyPlaces = { mode: ReplyMode; places: string[] };
export type ChannelReplyRules = { slack: ReplyPlaces; telegram: ReplyPlaces; keptFromUpgrade: boolean };

const WORDS: Record<ReplyChannel, { label: string; options: Record<ReplyMode, string>; kept: string; placesHint: string }> = {
  slack: {
    label: "Slack",
    options: {
      thread: "In the thread. Everyone in the channel can read the answer.",
      direct: "In a direct message to the person who asked. The thread gets a one-line note with no data.",
      listed: "In the thread in the channels listed below. By direct message in every other channel.",
    },
    kept: "OpenNeko answered in the thread before this upgrade, so it still does. Choose another option to keep answers private.",
    placesHint: "One channel ID per line, for example C0123ABCD. Slack shows the ID at the bottom of the channel details.",
  },
  telegram: {
    label: "Telegram",
    options: {
      thread: "In the group. Everyone in the group can read the answer.",
      direct: "In a private chat with the person who asked. The group gets a one-line note with no data.",
      listed: "In the group for the groups listed below. In a private chat in every other group.",
    },
    kept: "OpenNeko answered in the group before this upgrade, so it still does. Choose another option to keep answers private.",
    placesHint: "One group ID per line, for example -1001234567890 or @opsroom.",
  },
};

export function replyRuleLine(channel: ReplyChannel, mode: ReplyMode, kept: boolean): string {
  return kept && mode === "thread" ? WORDS[channel].kept : WORDS[channel].options[mode];
}

export function placeLines(text: string): string[] {
  const out: string[] = [];
  for (const line of text.split(/[\n,]/)) {
    const id = line.trim();
    if (id && !out.includes(id)) out.push(id);
  }
  return out;
}

export default function ChannelRepliesPanel() {
  const [rules, setRules] = useState<ChannelReplyRules | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);

  const load = useCallback(async () => {
    const result = await adminApi<ChannelReplyRules>("/api/v1/admin/channels/replies");
    if (result.ok) {
      setRules(result.body);
      setLoadError(null);
    } else {
      setLoadError(result.error);
    }
  }, []);

  useEffect(() => {
    const initial = window.setTimeout(() => void load(), 0);
    return () => window.clearTimeout(initial);
  }, [load]);

  return (
    <section className="settings-card">
      <div className="settings-card-head">
        <div className="min-w-0">
          <h2 className="settings-card-title">Where answers go</h2>
          <p className="settings-card-copy">
            When someone asks OpenNeko in a channel or group, the answer holds the data that person may read. Choose who
            sees it. Workflow outputs posted to a channel follow the same rule and go to the workflow owner. Each change
            goes in the audit log.
          </p>
        </div>
      </div>
      {loadError ? (
        <p className="border-t border-border py-5 text-sm text-danger">Could not load where answers go: {loadError}</p>
      ) : rules === null ? (
        <p className="border-t border-border py-5 text-sm text-text3">Loading…</p>
      ) : (
        <div className="border-t border-border">
          {(["slack", "telegram"] as const).map((channel) => (
            <div key={`${channel}:${rules[channel].mode}:${rules[channel].places.join(",")}`} className="border-b border-border py-5 last:border-b-0">
              <ReplyRuleForm channel={channel} rule={rules[channel]} keptFromUpgrade={rules.keptFromUpgrade} onSaved={setRules} />
            </div>
          ))}
        </div>
      )}
    </section>
  );
}

export function ReplyRuleForm({
  channel,
  rule,
  keptFromUpgrade,
  onSaved,
}: {
  channel: ReplyChannel;
  rule: ReplyPlaces;
  keptFromUpgrade: boolean;
  onSaved: (rules: ChannelReplyRules) => void;
}) {
  const words = WORDS[channel];
  const [mode, setMode] = useState<ReplyMode>(rule.mode);
  const [places, setPlaces] = useState(rule.places.join("\n"));
  const [busy, setBusy] = useState(false);
  const [status, setStatus] = useState<{ tone: "ok" | "error"; text: string } | null>(null);
  const list = placeLines(places);
  const changed = mode !== rule.mode || list.join("\n") !== rule.places.join("\n");

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setBusy(true);
    setStatus(null);
    const result = await adminApi<ChannelReplyRules>("/api/v1/admin/channels/replies", "PUT", { channel, mode, places: list });
    setBusy(false);
    if (!result.ok) {
      setStatus({ tone: "error", text: result.error });
      return;
    }
    onSaved(result.body);
  };

  return (
    <form onSubmit={(event) => void submit(event)} className="grid gap-4">
      <Field label={`${words.label}: answer a question asked in a group chat`} hint={replyRuleLine(channel, mode, keptFromUpgrade && mode === rule.mode)}>
        <NativeSelect value={mode} onChange={(event) => setMode(event.target.value as ReplyMode)} aria-label={`${words.label} answers`}>
          <option value="listed">Privately, except in listed {channel === "slack" ? "channels" : "groups"}</option>
          <option value="direct">Always privately</option>
          <option value="thread">Always in the {channel === "slack" ? "thread" : "group"}</option>
        </NativeSelect>
      </Field>
      {mode === "listed" ? (
        <Field label={channel === "slack" ? "Channels that get answers in the thread" : "Groups that get answers in the group"} hint={words.placesHint}>
          <Textarea value={places} rows={3} spellCheck={false} onChange={(event) => setPlaces(event.target.value)} />
        </Field>
      ) : null}
      <ActionGroup>
        <Button type="submit" variant="primary" disabled={busy || !changed}>
          {busy ? "Saving…" : "Save"}
        </Button>
        {status ? (
          <span role="status" className={`text-sm ${status.tone === "error" ? "text-danger" : "text-text2"}`}>
            {status.text}
          </span>
        ) : null}
      </ActionGroup>
    </form>
  );
}
