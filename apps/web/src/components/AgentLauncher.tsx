"use client";

import { FormEvent, useState } from "react";
import { useRouter } from "next/navigation";
import { ArrowUp } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import { Spinner } from "@/components/ui/spinner";

const STARTERS = [
  {
    label: "Investigate a change",
    prompt: "Investigate the most important change in the business today.",
  },
  {
    label: "Prepare a decision",
    prompt: "Prepare the decision that needs my attention most right now.",
  },
  {
    label: "Build a workflow",
    prompt: "Help me build a workflow for a recurring task.",
  },
];

function titleFromPrompt(prompt: string) {
  const firstLine = prompt.split("\n")[0]?.trim() ?? "";
  return firstLine.length > 72 ? `${firstLine.slice(0, 69)}…` : firstLine;
}

export default function AgentLauncher() {
  const router = useRouter();
  const [prompt, setPrompt] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function launch(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const nextPrompt = prompt.trim();
    if (!nextPrompt || submitting) return;

    setSubmitting(true);
    setError(null);

    try {
      const response = await fetch("/api/work/threads", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ title: titleFromPrompt(nextPrompt) }),
      });
      if (!response.ok) throw new Error("Could not open a new task");

      const payload = (await response.json()) as {
        thread?: { id?: string };
      };
      if (!payload.thread?.id) throw new Error("The new task has no thread");

      router.push(
        `/work/${payload.thread.id}?seed=${encodeURIComponent(nextPrompt)}`,
      );
    } catch (cause) {
      setError(
        cause instanceof Error
          ? cause.message
          : "OpenNeko could not start this task. Try again.",
      );
      setSubmitting(false);
    }
  }

  return (
    <section className="agent-launcher" aria-labelledby="agent-launcher-title">
      <div className="agent-launcher-intro">
        <div>
          <h2 id="agent-launcher-title">Start a task</h2>
          <p>Ask a question, investigate a change, or build a workflow.</p>
        </div>
      </div>

      <form className="agent-launcher-form" onSubmit={launch}>
        <label className="sr-only" htmlFor="agent-objective">
          Objective for OpenNeko
        </label>
        <Textarea
          id="agent-objective"
          value={prompt}
          onChange={(event) => {
            setPrompt(event.target.value);
            if (error) setError(null);
          }}
          placeholder="What should OpenNeko investigate or do?"
          rows={2}
          disabled={submitting}
        />
        <Button
          variant="ghost"
          size="icon"
          type="submit"
          className="agent-launcher-submit"
          disabled={!prompt.trim() || submitting}
          aria-label={submitting ? "Starting task" : "Start task"}
        >
          {submitting ? (
            <Spinner className="agent-launcher-spinner" aria-hidden="true" />
          ) : (
            <ArrowUp aria-hidden="true" />
          )}
        </Button>
      </form>

      <div className="agent-launcher-foot">
        <span>Start with</span>
        <div className="agent-starters">
          {STARTERS.map((starter) => (
            <Button
              variant="ghost"
              size="sm"
              key={starter.label}
              type="button"
              onClick={() => setPrompt(starter.prompt)}
              disabled={submitting}
            >
              {starter.label}
            </Button>
          ))}
        </div>
      </div>

      {error ? (
        <div className="agent-launcher-error" role="alert">
          {error}
        </div>
      ) : null}
    </section>
  );
}
