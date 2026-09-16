import "server-only";

import { randomUUID } from "node:crypto";
import { mkdir, readFile, realpath, stat, writeFile } from "node:fs/promises";
import { basename, extname, resolve, sep } from "node:path";
import {
  buildRecordImportPlan,
  getRecordImportRun,
  normalizeRecordImportSourcePath,
  type RecordImportPlan,
  type RecordImportStatus,
} from "@neko/records";
import { ensureOrgWorkspace, can, inProcessControlPlane } from "@neko/llm/work";
import { approveActionRequest } from "@neko/llm/workflows";
import { getCurrentActor } from "@/lib/actor";
import {
  getWebRecordsPool,
  loadRecordAppShell,
  RecordAppRouteError,
} from "@/lib/records";

const MAX_IMPORT_UPLOAD_BYTES = 25 * 1024 * 1024;

export type RecordImportAdminField = {
  apiName: string;
  label: string;
  kind: string;
  required: boolean;
};

export type RecordImportAdminObject = {
  apiName: string;
  label: string;
  pluralLabel: string;
  fields: RecordImportAdminField[];
};

export type RecordImportRunSummary = {
  id: string;
  objectApiName: string;
  sourceName: string;
  sourceRows: number;
  status: RecordImportStatus;
  stage: string | null;
  progress: Record<string, unknown> | null;
  report: Record<string, unknown> | null;
  error: Record<string, unknown> | null;
  createdAt: string;
  updatedAt: string;
};

export type RecordArtifactImportSummary = Record<string, unknown>;

export class RecordImportWebPermissionError extends Error {
  constructor(
    message = "Records imports require an administrator.",
    readonly code = "records_import_admin_required",
  ) {
    super(message);
    this.name = "RecordImportWebPermissionError";
  }
}

export class RecordImportWebExecutionError extends Error {
  readonly code = "records_import_action_failed";

  constructor(
    message: string,
    readonly actionRequestId: string,
  ) {
    super(message);
    this.name = "RecordImportWebExecutionError";
  }
}

async function requireImportAdmin() {
  const actor = await getCurrentActor();
  if (actor.role !== "admin") throw new RecordImportWebPermissionError();
  return actor;
}

function safeCsvName(name: string): string {
  const plain = basename(name).split("\\").pop() ?? name;
  const safe = plain
    .replace(/[^a-zA-Z0-9._-]/g, "_")
    .replace(/^\.+/, "_")
    .slice(0, 180);
  if (!safe || extname(safe).toLocaleLowerCase("en-US") !== ".csv") {
    throw new Error("Choose a .csv file.");
  }
  return safe;
}

async function readStagedImport(orgId: string, sourcePath: string): Promise<Uint8Array> {
  const relativePath = normalizeRecordImportSourcePath(sourcePath);
  const workspace = await ensureOrgWorkspace(orgId);
  const root = await realpath(workspace.orgRoot);
  const file = await realpath(resolve(root, relativePath));
  if (file !== root && !file.startsWith(`${root}${sep}`)) {
    throw new Error("Import source escapes its organization workspace.");
  }
  const metadata = await stat(file);
  if (!metadata.isFile()) throw new Error("Import source is not a regular file.");
  if (metadata.size > MAX_IMPORT_UPLOAD_BYTES) {
    throw new Error("Import source exceeds 25 MiB.");
  }
  return readFile(file);
}

export async function stageRecordImportUpload(input: {
  orgId: string;
  file: File;
}): Promise<{ sourcePath: string; sourceName: string; bytes: Uint8Array }> {
  await requireImportAdmin();
  if (input.file.size <= 0) throw new Error("Choose a non-empty CSV file.");
  if (input.file.size > MAX_IMPORT_UPLOAD_BYTES) {
    throw new Error("CSV uploads can be at most 25 MiB.");
  }
  const sourceName = safeCsvName(input.file.name || "import.csv");
  const bytes = new Uint8Array(await input.file.arrayBuffer());
  const workspace = await ensureOrgWorkspace(input.orgId);
  const runDirectory = randomUUID();
  const directory = resolve(workspace.orgRoot, "imports", runDirectory);
  await mkdir(directory, { recursive: true, mode: 0o700 });
  await writeFile(resolve(directory, sourceName), bytes, { mode: 0o600, flag: "wx" });
  return {
    sourcePath: `imports/${runDirectory}/${sourceName}`,
    sourceName,
    bytes,
  };
}

function runSummary(run: NonNullable<Awaited<ReturnType<typeof getRecordImportRun>>>): RecordImportRunSummary {
  return {
    id: run.id,
    objectApiName: run.plan.objectApiName,
    sourceName: run.plan.source.name,
    sourceRows: run.plan.rowCount,
    status: run.status,
    stage: run.currentStage,
    progress: run.progress,
    report: run.report,
    error: run.error,
    createdAt: run.createdAt.toISOString(),
    updatedAt: run.updatedAt.toISOString(),
  };
}

export async function getRecordImportAdminModel(input: {
  orgId: string;
  appId: string;
}): Promise<{
  appId: string;
  appLabel: string;
  objects: RecordImportAdminObject[];
  recentRuns: RecordImportRunSummary[];
  artifactImport: RecordArtifactImportSummary | null;
  importsEnabled: boolean;
}> {
  await requireImportAdmin();
  const shell = await loadRecordAppShell(input.orgId, input.appId);
  const artifactImport =
    typeof shell.config.import === "object" &&
    shell.config.import !== null &&
    !Array.isArray(shell.config.import)
      ? (shell.config.import as RecordArtifactImportSummary)
      : null;
  if (shell.availability !== "active" && !artifactImport) {
    throw new RecordAppRouteError(
      shell.degradedReason ?? "The records data plane is degraded.",
      503,
    );
  }
  const objects = shell.snapshot.objects
    .filter((object) => object.archivedAt === null)
    .map((object): RecordImportAdminObject => ({
      apiName: object.apiName,
      label: object.label,
      pluralLabel: object.pluralLabel,
      fields: shell.snapshot.fields
        .filter(
          (field) =>
            field.objectId === object.id &&
            field.archivedAt === null &&
            !field.readOnly,
        )
        .map((field) => ({
          apiName: field.apiName,
          label: field.label,
          kind: field.kind,
          required: field.required,
        })),
    }));
  const recent = await getWebRecordsPool().query<{ id: string }>(
    `select id from engine.import_run
     where org_id = $1 and app_id = $2
     order by created_at desc limit 10`,
    [input.orgId, shell.snapshot.app.appId],
  );
  const runs = await Promise.all(
    recent.rows.map((row) =>
      getRecordImportRun(getWebRecordsPool(), { orgId: input.orgId, id: row.id }),
    ),
  );
  return {
    appId: shell.snapshot.app.appId,
    appLabel: shell.snapshot.app.label,
    objects,
    recentRuns: runs.filter((run) => run !== null).map((run) => runSummary(run!)),
    artifactImport,
    importsEnabled: shell.availability === "active",
  };
}

export async function previewRecordImport(input: {
  orgId: string;
  appId: string;
  objectApiName: string;
  sourcePath: string;
  sourceName: string;
  bytes?: Uint8Array;
  mapping?: Record<string, string | null>;
  duplicateKey?: string;
  batchSize?: number;
  emptyTextColumns?: string[];
}): Promise<RecordImportPlan> {
  await requireImportAdmin();
  const shell = await loadRecordAppShell(input.orgId, input.appId);
  if (shell.availability !== "active") {
    throw new RecordAppRouteError(
      shell.degradedReason ?? "The records data plane is degraded.",
      503,
    );
  }
  const bytes = input.bytes ?? (await readStagedImport(input.orgId, input.sourcePath));
  return buildRecordImportPlan({
    snapshot: shell.snapshot,
    objectApiName: input.objectApiName,
    sourcePath: input.sourcePath,
    sourceName: input.sourceName,
    bytes,
    mapping: input.mapping,
    duplicateKey: input.duplicateKey,
    batchSize: input.batchSize,
    emptyTextColumns: input.emptyTextColumns,
  });
}

function importPayload(plan: RecordImportPlan): Record<string, unknown> {
  return {
    app: plan.appId,
    object: plan.objectApiName,
    source_path: plan.source.path,
    source_name: plan.source.name,
    duplicate_key: plan.duplicateKey,
    batch_size: plan.batchSize,
    owner_user_id: plan.ownerUserId,
    mapping: Object.fromEntries(
      plan.columns.map((column) => [column.sourceColumn, column.targetField]),
    ),
    import_plan: plan,
  };
}

async function executeApprovedImportControl(input: {
  orgId: string;
  kind: "records_import_start" | "records_import_cancel";
  target: string;
  payload: Record<string, unknown>;
  summary: string;
}): Promise<{
  status: "executed" | "queued";
  actionRequestId: string;
  outcome: Record<string, unknown> | null;
  policy: string;
}> {
  const actor = await requireImportAdmin();
  const decision = await inProcessControlPlane.evaluateActionPolicy({
    orgId: input.orgId,
    scope: "internal",
    kind: input.kind,
    target: input.target,
    riskLevel: input.kind === "records_import_start" ? "high" : "medium",
  });
  if (decision.decision === "no_policy") {
    throw new RecordImportWebPermissionError(
      "No enabled policy permits records imports for this organization.",
      "records_import_policy_missing",
    );
  }
  if (decision.decision === "deny") {
    throw new RecordImportWebPermissionError(decision.reason, "records_import_denied");
  }
  if (!can(actor, "approve", { kind: "action_approval", approverRole: null })) {
    throw new RecordImportWebPermissionError();
  }
  const request = await inProcessControlPlane.createActionRequest({
    orgId: input.orgId,
    scope: "internal",
    kind: input.kind,
    target: input.target,
    payload: input.payload,
    riskLevel: input.kind === "records_import_start" ? "high" : "medium",
    status: decision.mode === "draft_only" ? "draft" : "pending_approval",
    policyId: decision.policy.id,
    summary: input.summary,
    intent: `${input.summary}. The signed-in administrator explicitly submitted this control.`,
    actorUserId: actor.userId,
    actorRole: "admin",
    actorBackend: "web-import",
  });
  await approveActionRequest({
    id: request.id,
    orgId: input.orgId,
    approverUserId: actor.userId,
    approver: { userId: actor.userId, role: "admin" },
  });
  await inProcessControlPlane.enqueueActionExecute({
    orgId: input.orgId,
    actionRequestId: request.id,
  });
  const execution = await inProcessControlPlane.waitForActionExecution({
    orgId: input.orgId,
    actionRequestId: request.id,
    timeoutMs: 45_000,
  });
  if (execution.status === "failed") {
    throw new RecordImportWebExecutionError(execution.error, request.id);
  }
  if (execution.status === "timeout") {
    return {
      status: "queued",
      actionRequestId: request.id,
      outcome: null,
      policy: decision.policy.name,
    };
  }
  return {
    status: "executed",
    actionRequestId: request.id,
    outcome: execution.outcome.result ?? null,
    policy: decision.policy.name,
  };
}

export async function startRecordImportFromWeb(input: {
  orgId: string;
  plan: RecordImportPlan;
}) {
  const result = await executeApprovedImportControl({
    orgId: input.orgId,
    kind: "records_import_start",
    target: `record-import:${input.plan.appId}/${input.plan.objectApiName}`,
    payload: importPayload(input.plan),
    summary: `Import ${input.plan.rowCount.toLocaleString("en")} rows from ${input.plan.source.name} into ${input.plan.objectApiName}`,
  });
  return {
    ...result,
    importRunId:
      result.outcome && typeof result.outcome.importRunId === "string"
        ? result.outcome.importRunId
        : null,
  };
}

export async function getRecordImportStatusForWeb(input: {
  orgId: string;
  appId: string;
  importRunId: string;
}): Promise<RecordImportRunSummary | null> {
  await requireImportAdmin();
  const run = await getRecordImportRun(getWebRecordsPool(), {
    orgId: input.orgId,
    id: input.importRunId,
  });
  if (!run || run.appId !== input.appId) return null;
  return runSummary(run);
}

export async function cancelRecordImportFromWeb(input: {
  orgId: string;
  appId: string;
  importRunId: string;
}) {
  const run = await getRecordImportStatusForWeb(input);
  if (!run) throw new Error("Records import run was not found.");
  return executeApprovedImportControl({
    orgId: input.orgId,
    kind: "records_import_cancel",
    target: `record-import:${input.appId}/${run.objectApiName}`,
    payload: { import_run_id: input.importRunId },
    summary: `Cancel import of ${run.sourceName} into ${run.objectApiName}`,
  });
}
