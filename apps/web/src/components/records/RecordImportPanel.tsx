"use client";

import Link from "next/link";
import {
  Ban,
  CheckCircle2,
  FileSpreadsheet,
  LockKeyhole,
  Upload,
} from "lucide-react";
import { useEffect, useMemo, useState, type FormEvent } from "react";
import type { RecordImportPlan } from "@neko/records";
import type {
  RecordArtifactImportSummary,
  RecordImportAdminObject,
  RecordImportRunSummary,
} from "@/lib/records-imports";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { NativeSelect } from "@/components/ui/native-select";
import { Spinner } from "@/components/ui/spinner";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";

type ApiResponse = {
  error?: string;
  plan?: RecordImportPlan;
  status?: "executed" | "queued";
  actionRequestId?: string;
  importRunId?: string | null;
  run?: RecordImportRunSummary;
};

function statusLabel(status: RecordImportRunSummary["status"]): string {
  return status === "succeeded"
    ? "Complete"
    : status === "cancelled"
      ? "Cancelled"
      : status[0]!.toUpperCase() + status.slice(1);
}

function count(value: unknown): number {
  return typeof value === "number" && Number.isFinite(value) ? value : 0;
}

function record(value: unknown): Record<string, unknown> | null {
  return typeof value === "object" && value !== null && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : null;
}

function recordList(value: unknown): Array<Record<string, unknown>> {
  return Array.isArray(value)
    ? value.flatMap((entry) => {
        const parsed = record(entry);
        return parsed ? [parsed] : [];
      })
    : [];
}

export function RecordImportPanel({
  appId,
  objects,
  recentRuns,
  artifactImport,
  importsEnabled,
  initialObject,
}: {
  appId: string;
  objects: RecordImportAdminObject[];
  recentRuns: RecordImportRunSummary[];
  artifactImport: RecordArtifactImportSummary | null;
  importsEnabled: boolean;
  initialObject?: string;
}) {
  const firstObject =
    objects.find((object) => object.apiName === initialObject)?.apiName ??
    objects[0]?.apiName ??
    "";
  const [objectApiName, setObjectApiName] = useState(firstObject);
  const [plan, setPlan] = useState<RecordImportPlan | null>(null);
  const [mapping, setMapping] = useState<Record<string, string | null>>({});
  const [duplicateKey, setDuplicateKey] = useState("id");
  const [previewing, setPreviewing] = useState(false);
  const [starting, setStarting] = useState(false);
  const [cancelling, setCancelling] = useState(false);
  const [message, setMessage] = useState<string | null>(null);
  const [actionRequestId, setActionRequestId] = useState<string | null>(null);
  const [activeRun, setActiveRun] = useState<RecordImportRunSummary | null>(
    null,
  );

  const selectedObject = useMemo(
    () => objects.find((object) => object.apiName === objectApiName) ?? null,
    [objectApiName, objects],
  );
  const duplicateOptions = useMemo(
    () =>
      [
        "id",
        ...Object.values(mapping).filter((value): value is string =>
          Boolean(value),
        ),
      ].filter((value, index, all) => all.indexOf(value) === index),
    [mapping],
  );
  const activeRunId = activeRun?.id;
  const activeRunStatus = activeRun?.status;

  useEffect(() => {
    if (
      !activeRunId ||
      !activeRunStatus ||
      !["planned", "running"].includes(activeRunStatus)
    ) {
      return;
    }
    let cancelled = false;
    const poll = async () => {
      try {
        const response = await fetch(
          `/api/a/${encodeURIComponent(appId)}/imports/${encodeURIComponent(activeRunId)}`,
          { cache: "no-store" },
        );
        const payload = (await response.json()) as ApiResponse;
        if (!cancelled && response.ok && payload.run) setActiveRun(payload.run);
      } catch {
        // Keep the last authoritative status visible and retry on the next tick.
      }
    };
    void poll();
    const timer = setInterval(poll, 1_500);
    return () => {
      cancelled = true;
      clearInterval(timer);
    };
  }, [activeRunId, activeRunStatus, appId]);

  async function preview(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setPreviewing(true);
    setMessage(null);
    setActionRequestId(null);
    setPlan(null);
    const body = new FormData(event.currentTarget);
    body.set("object", objectApiName);
    try {
      const response = await fetch(
        `/api/a/${encodeURIComponent(appId)}/imports/preview`,
        {
          method: "POST",
          body,
        },
      );
      const payload = (await response.json()) as ApiResponse;
      if (!response.ok || !payload.plan) {
        setMessage(payload.error ?? "The CSV could not be inspected.");
        return;
      }
      setPlan(payload.plan);
      setMapping(
        Object.fromEntries(
          payload.plan.columns.map((column) => [
            column.sourceColumn,
            column.targetField,
          ]),
        ),
      );
      setDuplicateKey(payload.plan.duplicateKey);
    } catch {
      setMessage("The import service could not be reached.");
    } finally {
      setPreviewing(false);
    }
  }

  function changeMapping(sourceColumn: string, target: string) {
    const value = target || null;
    setMapping((current) => ({ ...current, [sourceColumn]: value }));
    if (
      duplicateKey !== "id" &&
      duplicateKey === mapping[sourceColumn] &&
      value !== duplicateKey
    ) {
      setDuplicateKey("id");
    }
  }

  async function startImport() {
    if (!plan) return;
    setStarting(true);
    setMessage(null);
    setActionRequestId(null);
    try {
      const response = await fetch(
        `/api/a/${encodeURIComponent(appId)}/imports/start`,
        {
          method: "POST",
          headers: { "content-type": "application/json" },
          body: JSON.stringify({
            object: plan.objectApiName,
            sourcePath: plan.source.path,
            sourceName: plan.source.name,
            mapping,
            duplicateKey,
            batchSize: plan.batchSize,
          }),
        },
      );
      const payload = (await response.json()) as ApiResponse;
      setActionRequestId(payload.actionRequestId ?? null);
      if (!response.ok) {
        setMessage(
          payload.error ?? "The governed import could not be started.",
        );
        return;
      }
      if (!payload.importRunId) {
        setMessage(
          "The start action is queued. No import was assumed; follow the action request for its durable run ID.",
        );
        return;
      }
      const statusResponse = await fetch(
        `/api/a/${encodeURIComponent(appId)}/imports/${encodeURIComponent(payload.importRunId)}`,
        { cache: "no-store" },
      );
      const statusPayload = (await statusResponse.json()) as ApiResponse;
      if (statusResponse.ok && statusPayload.run)
        setActiveRun(statusPayload.run);
      setMessage("The approved import is running in the worker.");
    } catch {
      setMessage(
        "The import service could not be reached. No success was assumed.",
      );
    } finally {
      setStarting(false);
    }
  }

  async function openRun(id: string) {
    setMessage(null);
    try {
      const response = await fetch(
        `/api/a/${encodeURIComponent(appId)}/imports/${encodeURIComponent(id)}`,
        { cache: "no-store" },
      );
      const payload = (await response.json()) as ApiResponse;
      if (response.ok && payload.run) setActiveRun(payload.run);
      else setMessage(payload.error ?? "The import run could not be loaded.");
    } catch {
      setMessage("The import status service could not be reached.");
    }
  }

  async function cancelImport() {
    if (!activeRun) return;
    setCancelling(true);
    setMessage(null);
    try {
      const response = await fetch(
        `/api/a/${encodeURIComponent(appId)}/imports/${encodeURIComponent(activeRun.id)}`,
        { method: "DELETE" },
      );
      const payload = (await response.json()) as ApiResponse;
      setActionRequestId(payload.actionRequestId ?? null);
      setMessage(
        response.ok
          ? "Cancellation was approved. The worker will stop at a batch boundary."
          : (payload.error ?? "The import could not be cancelled."),
      );
    } catch {
      setMessage("The cancellation service could not be reached.");
    } finally {
      setCancelling(false);
    }
  }

  const progress = activeRun?.progress;
  const report = activeRun?.report;
  const processed = count(progress?.processed);
  const total = count(progress?.total) || activeRun?.sourceRows || 0;
  const percent =
    total > 0 ? Math.min(100, Math.round((processed / total) * 100)) : 0;
  const artifactValidation = record(artifactImport?.validation);
  const artifactRows = recordList(artifactValidation?.rows);
  const artifactSamples = recordList(artifactValidation?.sampledChecksums);
  const artifactDangling = recordList(artifactValidation?.danglingReferences);
  const artifactPermissions = record(artifactValidation?.permissionCollapse);
  const artifactRoles = recordList(artifactPermissions?.roles);
  const artifactLiveRows = artifactRows.reduce(
    (sum, row) => sum + count(row.live),
    0,
  );
  const artifactVerifiedSamples = artifactSamples.reduce(
    (sum, sample) => sum + count(sample.verified),
    0,
  );
  const artifactDanglingCount = artifactDangling.reduce(
    (sum, reference) => sum + count(reference.dangling),
    0,
  );
  const artifactIntegrity = artifactValidation?.integrity;

  return (
    <div className="records-import-layout">
      <section className="records-import-main">
        {importsEnabled ? (
          <form className="records-import-upload" onSubmit={preview}>
            <div>
              <span className="records-eyebrow">Step 1 · Inspect</span>
              <h2>Choose a destination and CSV</h2>
              <p>
                The file is staged privately, parsed as RFC-4180, and hashed
                before review.
              </p>
            </div>
            <label>
              Destination object
              <NativeSelect
                name="object"
                value={objectApiName}
                onChange={(event) => {
                  setObjectApiName(event.target.value);
                  setPlan(null);
                }}
              >
                {objects.map((object) => (
                  <option value={object.apiName} key={object.apiName}>
                    {object.pluralLabel}
                  </option>
                ))}
              </NativeSelect>
            </label>
            <label>
              CSV file
              <Input name="file" type="file" accept=".csv,text/csv" required />
            </label>
            <Button
              className="records-primary-action"
              variant="primary"
              size="sm"
              type="submit"
              disabled={previewing || !objectApiName}
            >
              {previewing ? (
                <Spinner className="records-spin" aria-hidden="true" />
              ) : (
                <Upload aria-hidden="true" />
              )}
              {previewing ? "Inspecting…" : "Review mapping"}
            </Button>
          </form>
        ) : (
          <section className="records-import-principles">
            <Ban aria-hidden="true" />
            <h2>Imports paused</h2>
            <p>
              The connector validation report needs attention. Import controls
              stay closed until the app returns to an active state.
            </p>
          </section>
        )}

        {importsEnabled && plan && selectedObject && (
          <section className="records-import-review">
            <header>
              <div>
                <span className="records-eyebrow">Step 2 · Review</span>
                <h2>{plan.source.name}</h2>
                <p>
                  {plan.rowCount.toLocaleString("en")} rows ·{" "}
                  {plan.columns.length} columns · SHA-256{" "}
                  {plan.source.sha256.slice(0, 12)}…
                </p>
              </div>
              <FileSpreadsheet aria-hidden="true" />
            </header>
            <div className="records-import-mapping-scroll">
              <Table className="records-import-mapping">
                <TableHeader>
                  <TableRow>
                    <TableHead>CSV column</TableHead>
                    <TableHead>Sample</TableHead>
                    <TableHead>Detected</TableHead>
                    <TableHead>Destination field</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {plan.columns.map((column) => (
                    <TableRow key={column.sourceColumn}>
                      <TableCell>{column.sourceColumn}</TableCell>
                      <TableCell>
                        {plan.sampleRows[0]?.[column.sourceColumn] || (
                          <span className="records-null">Empty</span>
                        )}
                      </TableCell>
                      <TableCell>
                        <span className="records-import-kind">
                          {column.inferredKind}
                        </span>
                      </TableCell>
                      <TableCell>
                        <NativeSelect
                          value={mapping[column.sourceColumn] ?? ""}
                          onChange={(event) =>
                            changeMapping(
                              column.sourceColumn,
                              event.target.value,
                            )
                          }
                          aria-label={`Destination for ${column.sourceColumn}`}
                        >
                          <option value="">Ignore this column</option>
                          <option value="id">Record ID</option>
                          {selectedObject.fields.map((field) => (
                            <option value={field.apiName} key={field.apiName}>
                              {field.label}
                              {field.required ? " · required" : ""}
                            </option>
                          ))}
                        </NativeSelect>
                        {!mapping[column.sourceColumn] &&
                          column.suggestedField && (
                            <small>
                              Suggested new field: {column.suggestedField.label}{" "}
                              ({column.suggestedField.kind}); ignored until
                              added to the schema.
                            </small>
                          )}
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </div>
            <footer className="records-import-review-footer">
              <label>
                Duplicate key
                <NativeSelect
                  value={duplicateKey}
                  onChange={(event) => setDuplicateKey(event.target.value)}
                >
                  {duplicateOptions.map((field) => (
                    <option value={field} key={field}>
                      {field === "id" ? "Record ID" : field}
                    </option>
                  ))}
                </NativeSelect>
              </label>
              <p>
                <LockKeyhole aria-hidden="true" /> Insert-only. Existing keys
                are reported, never overwritten.
              </p>
              <Button
                className="records-primary-action"
                variant="primary"
                size="sm"
                onClick={startImport}
                disabled={starting}
              >
                {starting ? (
                  <Spinner className="records-spin" aria-hidden="true" />
                ) : (
                  <CheckCircle2 aria-hidden="true" />
                )}
                {starting
                  ? "Approving…"
                  : `Approve and import ${plan.rowCount.toLocaleString("en")} rows`}
              </Button>
            </footer>
          </section>
        )}

        {message && (
          <div className="records-form-message" role="status">
            <span>{message}</span>
            {actionRequestId && (
              <Link href={`/actions/${actionRequestId}`}>
                View action request
              </Link>
            )}
          </div>
        )}
      </section>

      <aside className="records-import-side">
        {artifactValidation && (
          <section
            className="records-import-artifact-report"
            aria-label="Connector import report"
          >
            <header>
              {artifactIntegrity === "passed" ? (
                <CheckCircle2 aria-hidden="true" />
              ) : (
                <Ban aria-hidden="true" />
              )}
              <div>
                <span className="records-eyebrow">Connector migration</span>
                <h2>
                  {artifactIntegrity === "passed"
                    ? "Integrity verified"
                    : "Needs attention"}
                </h2>
              </div>
            </header>
            <dl className="records-import-report">
              <div>
                <dt>Objects</dt>
                <dd>{artifactRows.length.toLocaleString("en")}</dd>
              </div>
              <div>
                <dt>Live rows</dt>
                <dd>{artifactLiveRows.toLocaleString("en")}</dd>
              </div>
              <div>
                <dt>Samples verified</dt>
                <dd>{artifactVerifiedSamples.toLocaleString("en")}</dd>
              </div>
              <div>
                <dt>Dangling references</dt>
                <dd>{artifactDanglingCount.toLocaleString("en")}</dd>
              </div>
              <div>
                <dt>Unmatched users</dt>
                <dd>
                  {count(artifactValidation.unmatchedUsers).toLocaleString(
                    "en",
                  )}
                </dd>
              </div>
            </dl>
            <p>
              Permissions collapsed to{" "}
              {artifactRoles.map((role) => String(role.role)).join(" and ") ||
                "admin and member"}
              .
            </p>
          </section>
        )}
        {activeRun ? (
          <section className="records-import-status">
            <header>
              <span
                className={`records-import-status-dot is-${activeRun.status}`}
              />
              <div>
                <span className="records-eyebrow">Import status</span>
                <h2>{statusLabel(activeRun.status)}</h2>
              </div>
            </header>
            <strong>{activeRun.sourceName}</strong>
            <span>
              {activeRun.objectApiName} ·{" "}
              {activeRun.sourceRows.toLocaleString("en")} source rows
            </span>
            {activeRun.status === "running" && (
              <div className="records-import-progress">
                <div>
                  <span style={{ transform: `scaleX(${percent / 100})` }} />
                </div>
                <small>
                  {processed.toLocaleString("en")} of{" "}
                  {total.toLocaleString("en")} processed
                </small>
              </div>
            )}
            {(activeRun.status === "succeeded" ||
              activeRun.status === "cancelled") && (
              <dl className="records-import-report">
                <div>
                  <dt>Inserted</dt>
                  <dd>{count(report?.inserted).toLocaleString("en")}</dd>
                </div>
                <div>
                  <dt>Not inserted</dt>
                  <dd>{count(report?.rejected).toLocaleString("en")}</dd>
                </div>
                <div>
                  <dt>Duplicates</dt>
                  <dd>{count(report?.duplicates).toLocaleString("en")}</dd>
                </div>
              </dl>
            )}
            {activeRun.error && (
              <p className="records-import-error">
                {String(activeRun.error.message ?? "Import failed.")}
              </p>
            )}
            {["planned", "running"].includes(activeRun.status) && (
              <Button
                variant="danger"
                size="sm"
                onClick={cancelImport}
                disabled={cancelling}
              >
                {cancelling ? (
                  <Spinner className="records-spin" aria-hidden="true" />
                ) : (
                  <Ban aria-hidden="true" />
                )}
                {cancelling ? "Cancelling…" : "Cancel at batch boundary"}
              </Button>
            )}
          </section>
        ) : (
          <section className="records-import-principles">
            <LockKeyhole aria-hidden="true" />
            <h2>One approval, durable batches</h2>
            <p>
              Rows move through the service-only GraphJin path. Every committed
              batch carries an audit entry and a same-transaction receipt.
            </p>
          </section>
        )}

        {recentRuns.length > 0 && (
          <section className="records-import-recent">
            <span className="records-eyebrow">Recent imports</span>
            {recentRuns.map((run) => (
              <Button
                variant="ghost"
                type="button"
                onClick={() => openRun(run.id)}
                key={run.id}
              >
                <span>{run.sourceName}</span>
                <small>
                  {run.objectApiName} · {statusLabel(run.status)}
                </small>
              </Button>
            ))}
          </section>
        )}
      </aside>
    </div>
  );
}
