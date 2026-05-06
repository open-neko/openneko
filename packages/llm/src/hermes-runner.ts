/**
 * Hermes Agent runner — spawn `hermes -z` for one-shot agent runs.
 *
 * Replaces AxCrew for snapshot generation. Hermes brings a real terminal,
 * persistent skills, multi-provider routing, and its own agent loop. We give
 * up Ax's typed signatures and per-call provider knobs (contextCache,
 * thinkingTokenBudget, showThoughts) — those now live in ~/.hermes/config.yaml.
 *
 * Prerequisites:
 *  - `hermes` and `graphjin` CLIs installed on PATH (one-time host setup)
 *  - host config files (graphjin client.json, ~/.hermes/config.yaml, .env)
 *    are written by `provisionHostConfig()` from DB state. The worker calls
 *    it on boot and the settings PUT handlers call it after every save —
 *    no manual shell script.
 *
 * Process model: one child process per call. Prompt is passed as the `-z`
 * positional argument (Hermes does not read the prompt from stdin). Argv
 * size on macOS/Linux is ~256KB–1MB, well above our typical ~50KB prompt.
 * Stdout is the model's final reply only — Hermes's `-z` flag suppresses
 * UI chrome.
 *
 * Scratch isolation: each spawn gets its own dir under `os.tmpdir()` as
 * cwd. When the caller passes a `tag` (we use processing_job.id) the dir
 * is named exactly that — easy to grep for in `/tmp` and tie back to a DB
 * row. Untagged calls fall back to `neko-hermes-XXXXXX`. The dir is
 * removed on every run (success or failure) so disks don't fill up.
 */

import { spawn } from "node:child_process";
import { mkdir, mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";

export type HermesRunOptions = {
  prompt: string;
  /** Hard cap on the spawn lifetime. Default 5 minutes. */
  timeoutMs?: number;
  /** Total attempts on failure (transport-level: spawn errors, non-zero exit, timeout). Default 1 retry. */
  retries?: number;
  /** Pipe Hermes stderr to this process's stderr. */
  debug?: boolean;
  /**
   * Identifier embedded in the scratch dir name (e.g. processing_job UUID).
   * Lets you tie a `/tmp/neko-hermes-<tag>-XXXX` dir back to a DB row when
   * KEEP_SCRATCH=true is on. Sanitised to filesystem-safe characters.
   */
  tag?: string;
};

export async function runHermes(opts: HermesRunOptions): Promise<string> {
  const { prompt, timeoutMs = 5 * 60_000, retries = 1, debug = false, tag } = opts;
  let lastErr: Error | undefined;
  for (let attempt = 0; attempt <= retries; attempt++) {
    try {
      return await spawnOnce(prompt, timeoutMs, debug, tag);
    } catch (e) {
      lastErr = e instanceof Error ? e : new Error(String(e));
      if (debug) {
        console.warn(`[hermes] attempt ${attempt + 1}/${retries + 1} failed: ${lastErr.message}`);
      }
    }
  }
  throw lastErr ?? new Error("hermes: unknown failure");
}

async function spawnOnce(
  prompt: string,
  timeoutMs: number,
  debug: boolean,
  tag: string | undefined,
): Promise<string> {
  const safeTag = tag?.replace(/[^a-zA-Z0-9_-]/g, "").slice(0, 64);
  const scratch = safeTag
    ? await (async () => {
        // Use the tag verbatim. On EEXIST (e.g. retry with KEEP_SCRATCH=true
        // having retained the previous run's dir) fall back to mkdtemp with
        // the tag as a prefix so we never overwrite an operator's keepalive.
        const exact = join(tmpdir(), safeTag);
        try {
          await mkdir(exact);
          return exact;
        } catch (e) {
          const code = (e as NodeJS.ErrnoException).code;
          if (code !== "EEXIST") throw e;
          return mkdtemp(join(tmpdir(), `${safeTag}-`));
        }
      })()
    : await mkdtemp(join(tmpdir(), "neko-hermes-"));
  // Scratch is always cleaned up. If you need to inspect it during a
  // debugging session, edit this constant locally.
  const keepScratch = false;

  return new Promise<string>((resolve, reject) => {
    const child = spawn("hermes", ["-z", prompt], {
      stdio: ["ignore", "pipe", "pipe"],
      cwd: scratch,
    });

    const stdoutChunks: Buffer[] = [];
    const stderrChunks: Buffer[] = [];
    let timer: NodeJS.Timeout | null = null;
    let settled = false;
    let cleaned = false;

    // Cleanup always waits for the child to actually exit (close event),
    // even on timeout. Otherwise SIGTERM + immediate rm would race the
    // child's still-open file handles in the scratch dir.
    const cleanup = () => {
      if (cleaned) return;
      cleaned = true;
      if (keepScratch) {
        if (debug) console.warn(`[hermes] kept scratch dir: ${scratch}`);
      } else {
        rm(scratch, { recursive: true, force: true }).catch(() => {});
      }
    };

    const settleResolve = (out: string) => {
      if (settled) return;
      settled = true;
      if (timer) clearTimeout(timer);
      resolve(out);
    };
    const settleReject = (err: Error) => {
      if (settled) return;
      settled = true;
      if (timer) clearTimeout(timer);
      reject(err);
    };

    child.stdout.on("data", (c: Buffer) => stdoutChunks.push(c));
    child.stderr.on("data", (c: Buffer) => {
      stderrChunks.push(c);
      if (debug) process.stderr.write(c);
    });

    child.on("error", (e) => {
      settleReject(new Error(`hermes spawn failed: ${e.message}`));
      // For spawn errors like ENOENT the close event may not fire — clean
      // up here as a fallback. Idempotent via the `cleaned` flag.
      cleanup();
    });

    child.on("close", (code) => {
      const stdout = Buffer.concat(stdoutChunks).toString("utf8");
      const stderr = Buffer.concat(stderrChunks).toString("utf8");
      cleanup();
      if (code !== 0) {
        settleReject(new Error(`hermes exited ${code}: ${stderr.slice(-500) || "(no stderr)"}`));
        return;
      }
      settleResolve(stdout);
    });

    timer = setTimeout(() => {
      child.kill("SIGTERM");
      settleReject(new Error(`hermes timed out after ${timeoutMs}ms`));
      // Don't cleanup here — wait for child.on("close") to fire once
      // SIGTERM lands. The child still has file handles open in scratch
      // until then.
    }, timeoutMs);
  });
}

const FENCE_RE = /^\s*```(?:json)?\s*([\s\S]*?)\s*```\s*$/;

/**
 * Tolerant JSON parser. Tries: raw → strip ```json fence → slice
 * first '{' to last '}'. Throws with a head-of-output excerpt on failure
 * so the caller can surface a useful error.
 */
export function parseJsonFromOutput(raw: string): unknown {
  const trimmed = raw.trim();
  const fenced = trimmed.match(FENCE_RE);
  const candidate = (fenced?.[1] ?? trimmed).trim();
  try {
    return JSON.parse(candidate);
  } catch {
    const first = candidate.indexOf("{");
    const last = candidate.lastIndexOf("}");
    if (first === -1 || last === -1 || last < first) {
      throw new Error(
        `hermes output not parseable as JSON (no object braces found): ${candidate.slice(0, 200)}`,
      );
    }
    return JSON.parse(candidate.slice(first, last + 1));
  }
}
