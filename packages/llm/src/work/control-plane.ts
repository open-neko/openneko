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
  /** ADM3: installed plugins (manifest) + marketplace catalog. */
  listPlugins(input: { orgId: string }): Promise<PluginCatalog>;
  /** ADM1: the org's users (id, email, role, disabled). */
  listUsers(input: { orgId: string }): Promise<{
    users: Array<{
      id: string;
      email: string;
      name: string | null;
      role: string;
      disabledAt: string | null;
      lastLoginAt: string | null;
    }>;
  }>;
  /** ADM5: channel workspaces (CH2) + identities (CH3) for chat-first channel management. */
  listChannels(input: { orgId: string }): Promise<{
    workspaces: Array<{
      channelPlugin: string;
      workspaceId: string;
      createdAt: string | null;
    }>;
    identities: Array<{
      id: string;
      channelPlugin: string;
      workspaceId: string;
      channelUserId: string;
      displayName: string | null;
      email: string | null;
      status: string;
      appUserId: string | null;
      firstSeenAt: string | null;
      verifiedAt: string | null;
    }>;
  }>;
}

export type PluginCatalog = {
  installed: Array<{
    name: string;
    version: string;
    source: string;
    network: string[];
    installedAt: string | null;
  }>;
  available: Array<{
    name: string;
    title: string;
    description: string;
    version: string;
  }>;
  marketplaceError?: string;
};

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

  async listPlugins(_input: { orgId: string }): Promise<PluginCatalog> {
    const {
      readManifest,
      createMarketplaceClient,
      OFFICIAL_MARKETPLACE_URL,
    } = await import("@open-neko/plugin-install");
    const manifest = await readManifest(process.cwd()).catch(() => null);
    const installed = (manifest?.plugins ?? []).map((p) => ({
      name: p.name,
      version: p.version,
      source: p.installSource ?? "marketplace",
      network: p.permissions?.network ?? [],
      installedAt: p.installedAt ?? null,
    }));
    let available: PluginCatalog["available"] = [];
    let marketplaceError: string | undefined;
    try {
      const marketplace = await createMarketplaceClient().fetch(
        OFFICIAL_MARKETPLACE_URL,
      );
      available = (marketplace.plugins ?? []).map((p) => {
        const latest = p.versions.find((v) => !v.yanked) ?? p.versions[0];
        return {
          name: p.name,
          title: p.title || p.name,
          description: p.description ?? "",
          version: latest?.version ?? "unknown",
        };
      });
    } catch (err) {
      marketplaceError = err instanceof Error ? err.message : String(err);
    }
    return {
      installed,
      available,
      ...(marketplaceError ? { marketplaceError } : {}),
    };
  }

  async listUsers(input: { orgId: string }) {
    const { app_user, db, eq } = await import("@neko/db");
    const rows = await db()
      .select({
        id: app_user.id,
        email: app_user.email,
        name: app_user.name,
        role: app_user.role,
        disabledAt: app_user.disabled_at,
        lastLoginAt: app_user.last_login_at,
      })
      .from(app_user)
      .where(eq(app_user.org_id, input.orgId));
    return {
      users: rows.map((r) => ({
        ...r,
        disabledAt: r.disabledAt?.toISOString() ?? null,
        lastLoginAt: r.lastLoginAt?.toISOString() ?? null,
      })),
    };
  }

  async listChannels(input: { orgId: string }) {
    const { channel_identity, channel_workspace, db, eq } = await import(
      "@neko/db"
    );
    const workspaces = await db()
      .select({
        channelPlugin: channel_workspace.channel_plugin,
        workspaceId: channel_workspace.workspace_id,
        createdAt: channel_workspace.created_at,
      })
      .from(channel_workspace)
      .where(eq(channel_workspace.org_id, input.orgId));
    const identities = await db()
      .select({
        id: channel_identity.id,
        channelPlugin: channel_identity.channel_plugin,
        workspaceId: channel_identity.workspace_id,
        channelUserId: channel_identity.channel_user_id,
        displayName: channel_identity.display_name,
        email: channel_identity.email,
        status: channel_identity.status,
        appUserId: channel_identity.app_user_id,
        firstSeenAt: channel_identity.first_seen_at,
        verifiedAt: channel_identity.verified_at,
      })
      .from(channel_identity)
      .where(eq(channel_identity.org_id, input.orgId));
    return {
      workspaces: workspaces.map((w) => ({
        ...w,
        createdAt: w.createdAt?.toISOString() ?? null,
      })),
      identities: identities.map((i) => ({
        ...i,
        firstSeenAt: i.firstSeenAt?.toISOString() ?? null,
        verifiedAt: i.verifiedAt?.toISOString() ?? null,
      })),
    };
  }
}

export const inProcessControlPlane: AgentControlPlane = new InProcessControlPlane();
