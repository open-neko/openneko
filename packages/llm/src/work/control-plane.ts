import { enqueue, QUEUE } from "@neko/db/jobs";
import {
  createActionRequest,
  listAllPolicies,
  listEnabledPolicies,
  upsertActionPolicyByName,
  type ActionPolicyRecord,
  type CreateActionPolicyInput,
  type UpsertActionPolicyResult,
} from "../workflows/action-store";
import { evaluateActionPolicy } from "../workflows/policy-engine";
import {
  saveWorkflowWithTrigger,
  type SaveWorkflowWithTriggerResult,
} from "../workflows/save-workflow-with-trigger";
import {
  listSubscriptionsByWorkflow,
  listWorkflows,
  type SaveWorkflowInput,
  type WorkflowRecord,
} from "../workflows/store";
import { rememberWorkMemory, searchWorkMemoryByContext } from "./memory";

type PolicyRequestSubject = Parameters<typeof evaluateActionPolicy>[0];
type PolicyDecision = ReturnType<typeof evaluateActionPolicy>;
type CreateActionRequestInput = Parameters<typeof createActionRequest>[0];
type RememberWorkMemoryInput = Parameters<typeof rememberWorkMemory>[0];
type WorkMemorySearchArgs = Parameters<typeof searchWorkMemoryByContext>[0];
type WorkMemorySearchResult = Awaited<
  ReturnType<typeof searchWorkMemoryByContext>
>[number];

/**
 * JSON-safe view of a control-plane result: what a value looks like after a
 * broker HTTP hop (Dates become ISO strings, undefined drops). The in-process
 * impl applies the same transform so both paths return identical shapes.
 */
export type Wire<T> = T extends Date
  ? string
  : T extends Array<infer U>
    ? Array<Wire<U>>
    : T extends object
      ? { [K in keyof T]: Wire<T[K]> }
      : T;

function toWire<T>(value: T): Wire<T> {
  return JSON.parse(JSON.stringify(value ?? null)) as Wire<T>;
}

export type WorkflowListEntry = Wire<WorkflowRecord> & {
  /** Enabled source_change trigger filter, if any. */
  when: Record<string, unknown> | null;
};

/**
 * The narrow control-plane surface an agent turn touches: policy eval,
 * action-request create + enqueue, the two memory ops, and the builder
 * tools' workflow/rule saves and lists. In-process today (direct DB/pg-boss).
 * The agent-sandbox path (Phase 2) injects an HTTP impl backed by the broker,
 * so the sandbox never holds DB creds or the pg-boss connection — the worker
 * stays the only gateway to those.
 */
export interface AgentControlPlane {
  evaluateActionPolicy(
    input: { orgId: string } & PolicyRequestSubject,
  ): Promise<PolicyDecision>;
  createActionRequest(input: CreateActionRequestInput): Promise<{ id: string }>;
  enqueueActionExecute(input: {
    orgId: string;
    actionRequestId: string;
  }): Promise<void>;
  rememberWorkMemory(input: RememberWorkMemoryInput): Promise<{ id: string }>;
  searchWorkMemoryByContext(
    args: WorkMemorySearchArgs,
  ): Promise<WorkMemorySearchResult[]>;
  saveWorkflowWithTrigger(
    input: SaveWorkflowInput,
  ): Promise<Wire<SaveWorkflowWithTriggerResult>>;
  listWorkflowsWithTriggers(input: {
    orgId: string;
    limit?: number;
  }): Promise<{ total: number; workflows: WorkflowListEntry[] }>;
  upsertActionPolicyByName(
    input: CreateActionPolicyInput,
  ): Promise<Wire<UpsertActionPolicyResult>>;
  listActionPolicies(input: {
    orgId: string;
    limit?: number;
  }): Promise<{ total: number; policies: Array<Wire<ActionPolicyRecord>> }>;
}

export class InProcessControlPlane implements AgentControlPlane {
  async evaluateActionPolicy(
    input: { orgId: string } & PolicyRequestSubject,
  ): Promise<PolicyDecision> {
    const { orgId, ...subject } = input;
    const policies = await listEnabledPolicies(orgId);
    return evaluateActionPolicy(subject, policies);
  }

  async createActionRequest(
    input: CreateActionRequestInput,
  ): Promise<{ id: string }> {
    const request = await createActionRequest(input);
    return { id: request.id };
  }

  async enqueueActionExecute(input: {
    orgId: string;
    actionRequestId: string;
  }): Promise<void> {
    await enqueue(QUEUE.ACTION_EXECUTE, input);
  }

  async rememberWorkMemory(
    input: RememberWorkMemoryInput,
  ): Promise<{ id: string }> {
    const memory = await rememberWorkMemory(input);
    return { id: memory.id };
  }

  async searchWorkMemoryByContext(
    args: WorkMemorySearchArgs,
  ): Promise<WorkMemorySearchResult[]> {
    return searchWorkMemoryByContext(args);
  }

  async saveWorkflowWithTrigger(
    input: SaveWorkflowInput,
  ): Promise<Wire<SaveWorkflowWithTriggerResult>> {
    return toWire(await saveWorkflowWithTrigger(input));
  }

  async listWorkflowsWithTriggers(input: {
    orgId: string;
    limit?: number;
  }): Promise<{ total: number; workflows: WorkflowListEntry[] }> {
    const all = await listWorkflows(input.orgId);
    const slice = all.slice(0, input.limit ?? 50);
    const triggers = await Promise.all(
      slice.map((w) => listSubscriptionsByWorkflow(input.orgId, w.id)),
    );
    return {
      total: all.length,
      workflows: slice.map((w, i) => {
        const dataTrigger = triggers[i].find(
          (s) => s.sourceKind === "source_change" && s.enabled,
        );
        return { ...toWire(w), when: dataTrigger ? dataTrigger.filter : null };
      }),
    };
  }

  async upsertActionPolicyByName(
    input: CreateActionPolicyInput,
  ): Promise<Wire<UpsertActionPolicyResult>> {
    return toWire(await upsertActionPolicyByName(input));
  }

  async listActionPolicies(input: {
    orgId: string;
    limit?: number;
  }): Promise<{ total: number; policies: Array<Wire<ActionPolicyRecord>> }> {
    const all = await listAllPolicies(input.orgId);
    return {
      total: all.length,
      policies: all.slice(0, input.limit ?? 50).map((p) => toWire(p)),
    };
  }
}

export const inProcessControlPlane: AgentControlPlane = new InProcessControlPlane();
