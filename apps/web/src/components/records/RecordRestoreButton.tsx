"use client";

import { useState } from "react";
import { useRouter } from "next/navigation";
import { RotateCcw } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Spinner } from "@/components/ui/spinner";

export function RecordRestoreButton({
  appId,
  objectApiName,
  recordId,
}: {
  appId: string;
  objectApiName: string;
  recordId: string;
}) {
  const router = useRouter();
  const [submitting, setSubmitting] = useState(false);
  const [message, setMessage] = useState<string | null>(null);

  async function restore() {
    if (submitting) return;
    setSubmitting(true);
    setMessage(null);
    try {
      const response = await fetch(
        `/api/a/${encodeURIComponent(appId)}/${encodeURIComponent(objectApiName)}/restore/${encodeURIComponent(recordId)}`,
        { method: "POST" },
      );
      const body = (await response.json()) as {
        error?: string;
        status?: "executed" | "queued";
        actionRequestId?: string;
      };
      if (!response.ok) throw new Error(body.error ?? "Restore failed");
      if (body.status === "executed") {
        router.replace(
          `/a/${appId}/${objectApiName}/${encodeURIComponent(recordId)}`,
        );
        router.refresh();
        return;
      }
      setMessage(
        body.actionRequestId
          ? `Restore queued · ${body.actionRequestId}`
          : "Restore queued",
      );
    } catch (error) {
      setMessage(error instanceof Error ? error.message : "Restore failed");
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <div className="records-restore-control">
      <Button
        className="records-primary-action"
        variant="primary"
        size="sm"
        onClick={restore}
        disabled={submitting}
      >
        {submitting ? (
          <Spinner className="records-spin" aria-hidden="true" />
        ) : (
          <RotateCcw aria-hidden="true" />
        )}
        {submitting ? "Restoring…" : "Restore"}
      </Button>
      {message && <span role="status">{message}</span>}
    </div>
  );
}
