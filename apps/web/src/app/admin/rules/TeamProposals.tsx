"use client";

import { useState } from "react";
import { toast } from "sonner";
import { ActionGroup } from "@/components/ui/action-group";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";

type DiffLine = { op: " " | "+" | "-"; text: string };

export type TeamProposal = {
  id: string;
  kind: "memory" | "skill" | "skill_lesson";
  target: string;
  title: string;
  files: Array<{ path: string; change: "added" | "changed" | "removed"; binary: boolean; lines: DiffLine[] }>;
  proposedBy: string | null;
  createdAt: string;
};

const kindLabel: Record<TeamProposal["kind"], string> = {
  memory: "team memory",
  skill: "team skill",
  skill_lesson: "skill lesson",
};

export default function TeamProposals({ proposals, onDecided }: { proposals: TeamProposal[]; onDecided: () => void }) {
  if (proposals.length === 0) return null;
  return (
    <section className="settings-card mt-6 grid gap-4" aria-labelledby="team-proposals-title">
      <div>
        <h2 id="team-proposals-title" className="settings-card-title">Proposed team changes</h2>
        <p className="settings-card-copy">
          Every member&apos;s run loads team memory and team skills. A change has no effect until you confirm it. Read each line first: text from a data row or a file can ask the agent to save an instruction.
        </p>
      </div>
      <ul className="m-0 grid list-none gap-4 p-0">
        {proposals.map((proposal) => (
          <ProposalRow key={proposal.id} proposal={proposal} onDecided={onDecided} />
        ))}
      </ul>
    </section>
  );
}

function ProposalRow({ proposal, onDecided }: { proposal: TeamProposal; onDecided: () => void }) {
  const [busy, setBusy] = useState<"confirm" | "reject" | null>(null);
  const [error, setError] = useState<string | null>(null);

  async function decide(verb: "confirm" | "reject") {
    setBusy(verb);
    setError(null);
    try {
      const response = await fetch(`/api/v1/admin/team-proposals/${encodeURIComponent(proposal.id)}/${verb}`, { method: "POST" });
      const body = await response.json().catch(() => ({}));
      if (!response.ok) throw new Error(body.error ?? `Could not ${verb} the change.`);
      toast.success(verb === "confirm" ? `${proposal.title}: done.` : `${proposal.title}: rejected.`);
      onDecided();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : `Could not ${verb} the change.`);
      if (verb === "confirm") onDecided();
    } finally {
      setBusy(null);
    }
  }

  return (
    <li className="grid gap-3 border-t border-border pt-4 first:border-t-0 first:pt-0">
      <div className="flex flex-wrap items-center gap-2">
        <span className="font-semibold text-text">{proposal.title}</span>
        <Badge variant="watch">{kindLabel[proposal.kind]}</Badge>
      </div>
      <p className="m-0 text-ui-body-sm text-text2">Proposed by {proposal.proposedBy ?? "the skill-learning job"}</p>
      {proposal.files.map((file) => (
        <div key={file.path} className="grid gap-1">
          <p className="m-0 text-ui-caption font-semibold text-text3">{file.path} · {file.change}</p>
          {file.binary ? (
            <p className="m-0 text-ui-body-sm text-text2">A binary file. OpenNeko does not show its content.</p>
          ) : (
            <pre className="m-0 max-h-80 overflow-auto whitespace-pre-wrap break-words rounded-[8px] bg-bg px-3 py-2.5 font-mono text-ui-caption text-text2">
              {file.lines.map((line, index) => (
                <div
                  key={index}
                  className={line.op === "+" ? "text-success" : line.op === "-" ? "text-danger" : "text-text2"}
                >
                  {line.op === " " ? "  " : `${line.op} `}
                  {line.text}
                </div>
              ))}
            </pre>
          )}
        </div>
      ))}
      {error ? <p role="alert" className="m-0 text-ui-body-sm text-danger">{error}</p> : null}
      <ActionGroup align="start">
        <Button size="sm" variant="primary" disabled={busy !== null} onClick={() => void decide("confirm")}>
          {busy === "confirm" ? "Confirming…" : "Confirm"}
        </Button>
        <Button size="sm" variant="ghost" disabled={busy !== null} onClick={() => void decide("reject")}>
          {busy === "reject" ? "Rejecting…" : "Reject"}
        </Button>
      </ActionGroup>
    </li>
  );
}
