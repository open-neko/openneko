import { readFile } from "node:fs/promises";
import { dirname, join } from "node:path";
import { parseDocument, stringify } from "yaml";
import { redactSourceConfig } from "./config-redact";

const STATE_FILE = ".openneko-graphjin-state";
const MAX_BYTES = 512 * 1024;

export type GraphjinRunState = {
  state: string;
  reason: string | null;
  since: string | null;
  restarts: number;
};

export type GraphjinConfigFileView = {
  path: string | null;
  yaml: string | null;
  error: string | null;
  state: GraphjinRunState | null;
};

function message(err: unknown): string {
  const code = (err as NodeJS.ErrnoException | null)?.code;
  if (code === "ENOENT") return "the file does not exist";
  if (code === "EACCES") return "OpenNeko may not read the file";
  return "OpenNeko could not read the file";
}

async function readRunState(configFile: string): Promise<GraphjinRunState | null> {
  try {
    const raw = await readFile(/* turbopackIgnore: true */ join(dirname(configFile), STATE_FILE), "utf8");
    const parsed = JSON.parse(raw) as Record<string, unknown>;
    if (typeof parsed.state !== "string") return null;
    const reason = typeof parsed.reason === "string" && parsed.reason ? parsed.reason : null;
    return {
      state: parsed.state,
      reason: reason ? (redactSourceConfig(reason) as string).slice(0, 500) : null,
      since: typeof parsed.since === "string" ? parsed.since : null,
      restarts: typeof parsed.restarts === "number" ? parsed.restarts : 0,
    };
  } catch {
    return null;
  }
}

/** Comments are dropped: a comment can hold a secret that no key names. */
export async function readGraphjinConfigFileView(
  configFile: string | null | undefined = process.env.OPENNEKO_GRAPHJIN_CONFIG,
): Promise<GraphjinConfigFileView> {
  const path = configFile?.trim() || null;
  if (!path) {
    return { path: null, yaml: null, error: "OPENNEKO_GRAPHJIN_CONFIG is not set", state: null };
  }
  const state = await readRunState(path);
  let raw: string;
  try {
    raw = await readFile(/* turbopackIgnore: true */ path, "utf8");
  } catch (err) {
    return { path, yaml: null, error: message(err), state };
  }
  if (Buffer.byteLength(raw) > MAX_BYTES) {
    return { path, yaml: null, error: "the file is larger than 512 KB", state };
  }
  const doc = parseDocument(raw, { uniqueKeys: false });
  if (doc.errors.length > 0) {
    return { path, yaml: null, error: `the file is not valid YAML (line ${doc.errors[0]!.linePos?.[0]?.line ?? "?"})`, state };
  }
  const redacted = redactSourceConfig(doc.toJS({ maxAliasCount: 100 }) ?? {});
  return { path, yaml: stringify(redacted, { lineWidth: 0 }), error: null, state };
}
