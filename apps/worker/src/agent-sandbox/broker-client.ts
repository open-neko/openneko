import type { AgentEvent } from "@neko/llm";
import type { AgentControlPlane } from "@neko/llm/work";

/**
 * AgentControlPlane implementation that posts to the broker over the egress
 * proxy. Used inside the agent sandbox (Phase 2c) so the agent reaches the
 * control plane through one narrow, authenticated channel instead of the DB.
 */
export class BrokerControlPlane implements AgentControlPlane {
  constructor(
    private readonly baseUrl: string,
    private readonly token: string,
  ) {}

  private async post<T>(path: string, body: unknown): Promise<T> {
    // Retry only when fetch REJECTS (connection never established — cold
    // egress-proxy path right after a bridge child spawns rejects the first
    // dial). HTTP error responses are never retried: the broker saw those.
    let lastErr: unknown;
    for (let attempt = 0; attempt < 3; attempt++) {
      if (attempt > 0) await new Promise((r) => setTimeout(r, 300 * attempt));
      let res: Response;
      try {
        res = await fetch(new URL(path, this.baseUrl), {
          method: "POST",
          headers: {
            "content-type": "application/json",
            authorization: `Bearer ${this.token}`,
          },
          body: JSON.stringify(body ?? {}),
        });
      } catch (err) {
        lastErr = err;
        continue;
      }
      if (!res.ok) {
        throw new Error(
          `broker ${path} -> ${res.status}: ${(await res.text()).slice(0, 200)}`,
        );
      }
      return (await res.json()) as T;
    }
    throw lastErr instanceof Error ? lastErr : new Error(String(lastErr));
  }

  evaluateActionPolicy(
    input: Parameters<AgentControlPlane["evaluateActionPolicy"]>[0],
  ): ReturnType<AgentControlPlane["evaluateActionPolicy"]> {
    return this.post("/v1/policy/evaluate", input);
  }

  createActionRequest(
    input: Parameters<AgentControlPlane["createActionRequest"]>[0],
  ): ReturnType<AgentControlPlane["createActionRequest"]> {
    return this.post("/v1/action/request", input);
  }

  async enqueueActionExecute(input: {
    orgId: string;
    actionRequestId: string;
  }): Promise<void> {
    await this.post("/v1/action/enqueue", input);
  }

  rememberWorkMemory(
    input: Parameters<AgentControlPlane["rememberWorkMemory"]>[0],
  ): ReturnType<AgentControlPlane["rememberWorkMemory"]> {
    return this.post("/v1/memory/remember", input);
  }

  searchWorkMemoryByContext(
    args: Parameters<AgentControlPlane["searchWorkMemoryByContext"]>[0],
  ): ReturnType<AgentControlPlane["searchWorkMemoryByContext"]> {
    return this.post("/v1/memory/search", args);
  }

  saveWorkflowWithTrigger(
    input: Parameters<AgentControlPlane["saveWorkflowWithTrigger"]>[0],
  ): ReturnType<AgentControlPlane["saveWorkflowWithTrigger"]> {
    return this.post("/v1/workflow/save", input);
  }

  listWorkflowsWithTriggers(
    input: Parameters<AgentControlPlane["listWorkflowsWithTriggers"]>[0],
  ): ReturnType<AgentControlPlane["listWorkflowsWithTriggers"]> {
    return this.post("/v1/workflow/list", input);
  }

  deleteWorkflow(
    input: Parameters<AgentControlPlane["deleteWorkflow"]>[0],
  ): ReturnType<AgentControlPlane["deleteWorkflow"]> {
    return this.post("/v1/workflow/delete", input);
  }

  upsertActionPolicyByName(
    input: Parameters<AgentControlPlane["upsertActionPolicyByName"]>[0],
  ): ReturnType<AgentControlPlane["upsertActionPolicyByName"]> {
    return this.post("/v1/rule/save", input);
  }

  listActionPolicies(
    input: Parameters<AgentControlPlane["listActionPolicies"]>[0],
  ): ReturnType<AgentControlPlane["listActionPolicies"]> {
    return this.post("/v1/rule/list", input);
  }

  listPlugins(
    input: Parameters<AgentControlPlane["listPlugins"]>[0],
  ): ReturnType<AgentControlPlane["listPlugins"]> {
    return this.post("/v1/plugins/list", input);
  }

  listUsers(
    input: Parameters<AgentControlPlane["listUsers"]>[0],
  ): ReturnType<AgentControlPlane["listUsers"]> {
    return this.post("/v1/users/list", input);
  }

  listChannels(
    input: Parameters<AgentControlPlane["listChannels"]>[0],
  ): ReturnType<AgentControlPlane["listChannels"]> {
    return this.post("/v1/channels/list", input);
  }

  listDataSources(
    input: Parameters<AgentControlPlane["listDataSources"]>[0],
  ): ReturnType<AgentControlPlane["listDataSources"]> {
    return this.post("/v1/datasources/list", input);
  }

  listAuditTrail(
    input: Parameters<AgentControlPlane["listAuditTrail"]>[0],
  ): ReturnType<AgentControlPlane["listAuditTrail"]> {
    return this.post("/v1/audit/list", input);
  }

  describeSourceGraph(
    input: Parameters<AgentControlPlane["describeSourceGraph"]>[0],
  ): ReturnType<AgentControlPlane["describeSourceGraph"]> {
    return this.post("/v1/source-graph/describe", input);
  }

  listSourceSecretNames(
    input: Parameters<AgentControlPlane["listSourceSecretNames"]>[0],
  ): ReturnType<AgentControlPlane["listSourceSecretNames"]> {
    return this.post("/v1/source-secrets/names", input);
  }
}

/** Emit agent events back to the host through the broker (one-way sink). */
export async function postAgentEvents(
  baseUrl: string,
  token: string,
  events: AgentEvent[],
): Promise<void> {
  const res = await fetch(new URL("/v1/events", baseUrl), {
    method: "POST",
    headers: {
      "content-type": "application/json",
      authorization: `Bearer ${token}`,
    },
    body: JSON.stringify({ events }),
  });
  if (!res.ok) {
    throw new Error(`broker /v1/events -> ${res.status}`);
  }
}
