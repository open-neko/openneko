const PACK_ID = /^[a-z0-9]+(?:-[a-z0-9]+)*$/;

export function validPackId(packId: string): boolean {
  return PACK_ID.test(packId);
}

function workerAdminBase(): string {
  return (process.env.WORKER_ADMIN_URL ?? "http://127.0.0.1:4100").replace(/\/+$/, "");
}

export async function requestPackWorker(
  path: string,
  init: RequestInit = {},
): Promise<{ status: number; body: unknown }> {
  let response: Response;
  try {
    response = await fetch(`${workerAdminBase()}${path}`, {
      ...init,
      cache: "no-store",
      signal: AbortSignal.any([AbortSignal.timeout(120_000), ...(init.signal ? [init.signal] : [])]),
    });
  } catch (error) {
    if (error instanceof TypeError) return { status: 502, body: { error: "worker is unavailable" } };
    throw error;
  }
  const body = await response.json().catch(() => ({ error: `Worker returned HTTP ${response.status}` }));
  return { status: response.status, body };
}
