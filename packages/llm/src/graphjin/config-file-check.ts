import { randomUUID } from "node:crypto";
import { request } from "node:http";
import { mkdir, readFile, rm, writeFile } from "node:fs/promises";
import { basename, dirname, join } from "node:path";
import { mergeGraphjinSourceConfigUpdate } from "./persist-source-config";

export const GRAPHJIN_CHECK_DIR = ".openneko-check";

export type GraphjinFileCheck =
  | { checked: false; reason: string }
  | { checked: true; valid: boolean; errors: string[] };

function postCandidate(socketPath: string, candidate: string): Promise<{ valid: boolean; errors: string[] }> {
  return new Promise((resolve, reject) => {
    const body = JSON.stringify({ candidate });
    const req = request(
      {
        socketPath,
        path: "/v1/graphjin/config-check",
        method: "POST",
        headers: { "content-type": "application/json", "content-length": Buffer.byteLength(body) },
        timeout: 15_000,
      },
      (res) => {
        const chunks: Buffer[] = [];
        res.on("data", (chunk: Buffer) => chunks.push(chunk));
        res.on("end", () => {
          const text = Buffer.concat(chunks).toString("utf8");
          if (res.statusCode !== 200) {
            reject(new Error(`config check returned ${res.statusCode}: ${text.trim().slice(0, 200)}`));
            return;
          }
          try {
            const parsed = JSON.parse(text) as { valid?: unknown; errors?: unknown };
            resolve({
              valid: parsed.valid === true,
              errors: Array.isArray(parsed.errors) ? parsed.errors.map(String) : [],
            });
          } catch {
            reject(new Error("config check returned no JSON"));
          }
        });
      },
    );
    req.on("timeout", () => req.destroy(new Error("config check timed out")));
    req.on("error", reject);
    req.end(body);
  });
}

/**
 * Write the config as the approval worker would after this update, and ask
 * serve to load it with the pinned GraphJin loader. The candidate lives in a
 * private folder beside the config and is deleted before this returns.
 */
export async function checkGraphjinConfigCandidate(input: {
  configFile?: string | null;
  socketPath?: string | null;
  update: Record<string, unknown>;
}): Promise<GraphjinFileCheck> {
  const configFile = input.configFile?.trim();
  const socketPath = input.socketPath?.trim();
  if (!configFile) return { checked: false, reason: "OPENNEKO_GRAPHJIN_CONFIG is not set" };
  if (!socketPath) return { checked: false, reason: "OPENNEKO_CONFIG_CHECK_SOCKET is not set" };
  const raw = await readFile(/* turbopackIgnore: true */ configFile, "utf8");
  const merged = mergeGraphjinSourceConfigUpdate(raw, input.update);
  const id = randomUUID();
  const root = join(/* turbopackIgnore: true */ dirname(configFile), GRAPHJIN_CHECK_DIR);
  const dir = join(/* turbopackIgnore: true */ root, id);
  await mkdir(/* turbopackIgnore: true */ root, { recursive: true, mode: 0o700 });
  await mkdir(/* turbopackIgnore: true */ dir, { mode: 0o700 });
  try {
    await writeFile(/* turbopackIgnore: true */ join(dir, basename(configFile)), merged, {
      mode: 0o600,
      flag: "wx",
    });
    const result = await postCandidate(socketPath, id);
    return { checked: true, ...result };
  } finally {
    await rm(/* turbopackIgnore: true */ dir, { recursive: true, force: true });
  }
}
