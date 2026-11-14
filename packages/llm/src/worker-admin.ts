import type { Agent } from "undici";

const DEFAULT_WORKER_ADMIN_URL = "http://127.0.0.1:4100";
const SOCKET_ORIGIN = "http://worker";

export interface WorkerAdminTarget {
  origin: string;
  socketPath?: string;
}

export function workerAdminTarget(raw: string | undefined = process.env.WORKER_ADMIN_URL): WorkerAdminTarget {
  const value = (raw?.trim() || DEFAULT_WORKER_ADMIN_URL).replace(/\/+$/, "");
  if (!value.startsWith("unix:")) return { origin: value };
  const socketPath = value.replace(/^unix:(\/\/)?/, "");
  if (!socketPath.startsWith("/")) throw new Error(`WORKER_ADMIN_URL ${value}: a unix address needs an absolute socket path`);
  return { origin: SOCKET_ORIGIN, socketPath };
}

const agents = new Map<string, Agent>();

export async function workerAdminFetch(path: string, init: RequestInit = {}, raw?: string): Promise<Response> {
  const target = workerAdminTarget(raw ?? process.env.WORKER_ADMIN_URL);
  if (!target.socketPath) return fetch(target.origin + path, init);
  const undici = await import("undici");
  let agent = agents.get(target.socketPath);
  if (!agent) {
    agent = new undici.Agent({ connect: { socketPath: target.socketPath } });
    agents.set(target.socketPath, agent);
  }
  const { cache: _cache, ...rest } = init;
  return (await undici.fetch(target.origin + path, { ...rest, dispatcher: agent } as Parameters<typeof undici.fetch>[1])) as unknown as Response;
}
