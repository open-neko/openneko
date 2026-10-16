import { mkdir, readFile, rename, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { contentDigest } from "./canonical";
import { EpisodeSchema, type EvalConfig, type EvalEpisode } from "./schemas";

export type EpisodeCacheKeyInput = {
  runnerVersion: string;
  caseContentId: string;
  phase: string;
  repetition: number;
  datasetDigest: string;
  oracleDigest: string;
  scorerDigest: string;
  /** Defaults that shape one episode; order, repetitions and concurrency do not. */
  defaults: Pick<EvalConfig["defaults"], "timeout" | "max_attempts" | "cache_state" | "content_capture">;
  /** Driver identity of the variant and the agent code path. */
  identity: unknown;
};

export function episodeCacheKey(input: EpisodeCacheKeyInput): string {
  return contentDigest({ schema: "openneko.eval.episode-cache-key/v1", ...input });
}

type CacheEntry = {
  schemaVersion: "openneko.eval.episode-cache/v1";
  key: string;
  inputs: EpisodeCacheKeyInput;
  episode: EvalEpisode;
};

/** Terminal episodes keyed by every input that shapes them. A changed input is a different key. */
export class EpisodeCache {
  constructor(readonly root: string) {}

  private path(key: string): string {
    return join(this.root, `${key.replace(/^sha256:/u, "")}.json`);
  }

  async read(key: string): Promise<EvalEpisode | undefined> {
    const text = await readFile(this.path(key), "utf8").catch(() => null);
    if (text === null) return undefined;
    try {
      const entry = JSON.parse(text) as CacheEntry;
      if (entry.schemaVersion !== "openneko.eval.episode-cache/v1" || entry.key !== key) return undefined;
      if (episodeCacheKey(entry.inputs) !== key) return undefined;
      const episode = EpisodeSchema.parse(entry.episode);
      const { integrityDigest, ...unsigned } = episode;
      return contentDigest(unsigned) === integrityDigest ? episode : undefined;
    } catch {
      return undefined;
    }
  }

  async write(inputs: EpisodeCacheKeyInput, episode: EvalEpisode): Promise<string> {
    const key = episodeCacheKey(inputs);
    await mkdir(this.root, { recursive: true });
    const target = this.path(key);
    const temporary = `${target}.${process.pid}.${Date.now()}.tmp`;
    const entry: CacheEntry = { schemaVersion: "openneko.eval.episode-cache/v1", key, inputs, episode };
    await writeFile(temporary, `${JSON.stringify(entry)}\n`, "utf8");
    await rename(temporary, target);
    return key;
  }
}
